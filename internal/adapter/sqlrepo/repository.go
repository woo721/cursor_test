package sqlrepo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/shopspring/decimal"

	"github.com/woo721/cursor_test/internal/domain"
)

// DB 是 database/sql 查询执行的最小端口，便于 sqlmock 与后续真实连接注入。
type DB interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	PingContext(context.Context) error
}

// Repository 在固定 Builder 之上执行聚合查询并映射为领域结果。
type Repository struct {
	source  domain.Source
	db      DB
	builder Builder
}

// New 构造共享 SQL 仓库；source 仅用于错误上下文，不参与 SQL 方言选择（方言由 builder 决定）。
func New(source domain.Source, db DB, builder Builder) *Repository {
	return &Repository{source: source, db: db, builder: builder}
}

func (r *Repository) wrapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	// 不附带 SQL 文本或参数，避免泄露查询细节到上游错误链。
	return fmt.Errorf("%s: %w", r.source, domain.ErrDependencyUnavailable)
}

func (r *Repository) RetailSummary(ctx context.Context, q domain.Query) (domain.RetailSummary, error) {
	stmt := r.builder.Retail(q)
	row := r.db.QueryRowContext(ctx, stmt.SQL, stmt.Args...)
	var (
		orderCount  int64
		salesAmount string
	)
	if err := row.Scan(&orderCount, &salesAmount); err != nil {
		return domain.RetailSummary{}, r.wrapErr(err)
	}
	amount, err := decimal.NewFromString(salesAmount)
	if err != nil {
		return domain.RetailSummary{}, r.wrapErr(err)
	}
	return domain.RetailSummary{OrderCount: orderCount, SalesAmount: amount}, nil
}

func (r *Repository) RenovationFunnel(ctx context.Context, q domain.Query) (domain.RenovationFunnel, error) {
	stmt := r.builder.Renovation(q)
	row := r.db.QueryRowContext(ctx, stmt.SQL, stmt.Args...)
	var f domain.RenovationFunnel
	if err := row.Scan(
		&f.LeadCount,
		&f.InvitedCount,
		&f.MeasuredCount,
		&f.SignedCount,
		&f.StartedCount,
		&f.CompletedCount,
	); err != nil {
		return domain.RenovationFunnel{}, r.wrapErr(err)
	}
	return f, nil
}

func (r *Repository) Ping(ctx context.Context) error {
	return r.wrapErr(r.db.PingContext(ctx))
}
