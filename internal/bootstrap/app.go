package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/sync/errgroup"

	"github.com/woo721/cursor_test/internal/adapter/memory"
	"github.com/woo721/cursor_test/internal/adapter/sqlrepo"
	"github.com/woo721/cursor_test/internal/app"
	"github.com/woo721/cursor_test/internal/config"
	"github.com/woo721/cursor_test/internal/database"
	"github.com/woo721/cursor_test/internal/domain"
	"github.com/woo721/cursor_test/internal/httpapi"
	"github.com/woo721/cursor_test/internal/observability"
)

const defaultStartupTimeout = 10 * time.Second

type Application struct {
	Handler   http.Handler
	closers   []io.Closer
	closeOnce sync.Once
	closeErr  error
}

type ownedDB interface {
	sqlrepo.DB
	io.Closer
}

type builders struct {
	openHive       func(config.Hive) (ownedDB, error)
	openTrino      func(config.Trino) (ownedDB, error)
	openStarRocks  func(config.StarRocks) (ownedDB, error)
	startupTimeout time.Duration
	logger         *slog.Logger
}

func Build(cfg config.Config) (*Application, error) {
	return buildWith(cfg, defaultBuilders())
}

func (a *Application) Close() error {
	if a == nil {
		return nil
	}
	a.closeOnce.Do(func() {
		// Application 拥有真实模式打开的数据库连接，关闭时按创建顺序反向释放。
		for i := len(a.closers) - 1; i >= 0; i-- {
			if err := a.closers[i].Close(); err != nil {
				a.closeErr = errors.Join(a.closeErr, err)
			}
		}
	})
	return a.closeErr
}

func defaultBuilders() builders {
	return builders{
		openHive: func(cfg config.Hive) (ownedDB, error) {
			return database.OpenHive(cfg)
		},
		openTrino: func(cfg config.Trino) (ownedDB, error) {
			return database.OpenTrino(cfg)
		},
		openStarRocks: func(cfg config.StarRocks) (ownedDB, error) {
			return database.OpenStarRocks(cfg)
		},
		startupTimeout: defaultStartupTimeout,
		logger:         slog.Default(),
	}
}

func buildWith(cfg config.Config, b builders) (*Application, error) {
	if b.startupTimeout <= 0 {
		b.startupTimeout = defaultStartupTimeout
	}
	if b.logger == nil {
		b.logger = slog.Default()
	}

	reg := prometheus.NewRegistry()
	metrics := observability.NewMetrics(reg)

	repos, closers, err := repositories(cfg, b)
	if err != nil {
		return nil, err
	}
	application := &Application{closers: closers}
	cleanup := func(err error) (*Application, error) {
		return nil, errors.Join(err, application.Close())
	}

	if cfg.DataMode == "real" {
		if err := pingRepositories(context.Background(), repos, b.startupTimeout); err != nil {
			return cleanup(fmt.Errorf("startup readiness: %w", err))
		}
	}

	historical, err := domain.ParseSource(cfg.Query.HistoricalSource)
	if err != nil || (historical != domain.SourceHive && historical != domain.SourceTrino) {
		return cleanup(fmt.Errorf("historical source: %w", domain.ErrInvalidArgument))
	}
	service, err := app.NewQueryService(
		repos,
		historical,
		cfg.Query.RealtimeWindowDays,
		cfg.Server.Timezone,
		app.SystemClock{},
		metrics,
	)
	if err != nil {
		return cleanup(err)
	}

	ready := readiness{repositories: uniqueRepositories(repos)}
	handler := httpapi.New(httpapi.Dependencies{
		Queries:        service,
		Ready:          ready,
		APIKey:         cfg.APIKey,
		QueryTimeout:   cfg.Query.Timeout,
		MaxQueryDays:   cfg.Query.MaxQueryDays,
		Timezone:       cfg.Server.Timezone,
		Logger:         b.logger,
		MetricsHandler: metrics.Handler(reg),
	})
	application.Handler = metrics.HTTPMiddleware(handler)
	return application, nil
}

func repositories(cfg config.Config, b builders) (map[domain.Source]domain.Repository, []io.Closer, error) {
	switch cfg.DataMode {
	case "memory", "":
		repo := memory.New()
		// 内存模式把三种公开数据源映射到同一个确定性仓库，保证本地启动不访问网络。
		return map[domain.Source]domain.Repository{
			domain.SourceHive:      repo,
			domain.SourceTrino:     repo,
			domain.SourceStarRocks: repo,
		}, nil, nil
	case "real":
		return realRepositories(cfg, b)
	default:
		return nil, nil, fmt.Errorf("DATA_MODE: must be memory or real")
	}
}

func realRepositories(cfg config.Config, b builders) (map[domain.Source]domain.Repository, []io.Closer, error) {
	var closers []io.Closer
	closeOnError := func(err error) (map[domain.Source]domain.Repository, []io.Closer, error) {
		return nil, nil, errors.Join(err, closeAll(closers))
	}

	hiveDB, err := b.openHive(cfg.Hive)
	if err != nil {
		return closeOnError(fmt.Errorf("open hive: %w", err))
	}
	closers = append(closers, hiveDB)
	trinoDB, err := b.openTrino(cfg.Trino)
	if err != nil {
		return closeOnError(fmt.Errorf("open trino: %w", err))
	}
	closers = append(closers, trinoDB)
	starRocksDB, err := b.openStarRocks(cfg.StarRocks)
	if err != nil {
		return closeOnError(fmt.Errorf("open starrocks: %w", err))
	}
	closers = append(closers, starRocksDB)

	hiveBuilder, err := sqlrepo.NewHiveBuilder(cfg.Tables.Prefix, cfg.Tables.Retail, cfg.Tables.Renovation)
	if err != nil {
		return closeOnError(err)
	}
	boundBuilder, err := sqlrepo.NewBoundBuilder(cfg.Tables.Prefix, cfg.Tables.Retail, cfg.Tables.Renovation)
	if err != nil {
		return closeOnError(err)
	}

	return map[domain.Source]domain.Repository{
		domain.SourceHive:      sqlrepo.New(domain.SourceHive, hiveDB, hiveBuilder),
		domain.SourceTrino:     sqlrepo.New(domain.SourceTrino, trinoDB, boundBuilder),
		domain.SourceStarRocks: sqlrepo.New(domain.SourceStarRocks, starRocksDB, boundBuilder),
	}, closers, nil
}

func closeAll(closers []io.Closer) error {
	var out error
	for i := len(closers) - 1; i >= 0; i-- {
		out = errors.Join(out, closers[i].Close())
	}
	return out
}

type readiness struct {
	repositories []domain.Repository
}

func (r readiness) Ping(ctx context.Context) error {
	for _, repo := range r.repositories {
		if err := repo.Ping(ctx); err != nil {
			return err
		}
	}
	return nil
}

func pingRepositories(ctx context.Context, repos map[domain.Source]domain.Repository, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	group, gctx := errgroup.WithContext(ctx)
	for _, repo := range uniqueRepositories(repos) {
		repo := repo
		group.Go(func() error {
			return repo.Ping(gctx)
		})
	}
	return group.Wait()
}

func uniqueRepositories(repos map[domain.Source]domain.Repository) []domain.Repository {
	out := make([]domain.Repository, 0, len(repos))
	for _, source := range []domain.Source{domain.SourceHive, domain.SourceTrino, domain.SourceStarRocks} {
		repo := repos[source]
		if repo == nil {
			continue
		}
		seen := false
		for _, existing := range out {
			if repo == existing {
				seen = true
				break
			}
		}
		if !seen {
			out = append(out, repo)
		}
	}
	return out
}

var _ ownedDB = (*sql.DB)(nil)
