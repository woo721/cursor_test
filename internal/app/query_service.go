package app

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/shopspring/decimal"
	"golang.org/x/sync/errgroup"

	"github.com/woo721/cursor_test/internal/domain"
)

type Clock interface{ Now() time.Time }

type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now() }

type QueryService struct {
	repositories map[domain.Source]domain.Repository
	historical   domain.Source
	realtimeDays int
	timezone     *time.Location
	clock        Clock
	observer     QueryObserver
}

type RetailResult struct {
	Data     domain.RetailSummary
	Segments []domain.Segment
}

type RenovationResult struct {
	Data     domain.RenovationFunnel
	Segments []domain.Segment
}

// NewQueryService 构造查询编排服务；历史源仅允许 Hive/Trino，且必须具备历史与 StarRocks 仓库。
func NewQueryService(
	repositories map[domain.Source]domain.Repository,
	historical domain.Source,
	realtimeDays int,
	timezone *time.Location,
	clock Clock,
	observer QueryObserver,
) (*QueryService, error) {
	if historical != domain.SourceHive && historical != domain.SourceTrino {
		return nil, domain.ErrInvalidArgument
	}
	if realtimeDays < 1 {
		return nil, domain.ErrInvalidArgument
	}
	if timezone == nil || clock == nil || observer == nil {
		return nil, domain.ErrInvalidArgument
	}
	if repositories == nil {
		return nil, domain.ErrInvalidArgument
	}
	if repositories[historical] == nil || repositories[domain.SourceStarRocks] == nil {
		return nil, domain.ErrInvalidArgument
	}
	return &QueryService{
		repositories: repositories,
		historical:   historical,
		realtimeDays: realtimeDays,
		timezone:     timezone,
		clock:        clock,
		observer:     observer,
	}, nil
}

func beginningOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// Plan 按请求源与实时窗口切分不重叠分段。
// 实时截止日 = 业务日今天减去 (realtimeDays-1)；跨边界时历史段止于 cutoff-1，StarRocks 起于 cutoff。
func (s *QueryService) Plan(requested domain.Source, r domain.DateRange) ([]domain.Segment, error) {
	switch requested {
	case domain.SourceHive, domain.SourceTrino, domain.SourceStarRocks:
		return []domain.Segment{{Source: requested, Range: r}}, nil
	case domain.SourceAuto:
		// ok
	default:
		return nil, domain.ErrInvalidArgument
	}

	cutoff := beginningOfDay(s.clock.Now().In(s.timezone)).
		AddDate(0, 0, -(s.realtimeDays - 1))

	if r.End.Before(cutoff) {
		return []domain.Segment{{Source: s.historical, Range: r}}, nil
	}
	if !r.Start.Before(cutoff) {
		return []domain.Segment{{Source: domain.SourceStarRocks, Range: r}}, nil
	}

	historyEnd := cutoff.AddDate(0, 0, -1)
	return []domain.Segment{
		{Source: s.historical, Range: domain.DateRange{Start: r.Start, End: historyEnd}},
		{Source: domain.SourceStarRocks, Range: domain.DateRange{Start: cutoff, End: r.End}},
	}, nil
}

// RetailSummary 并发查询各分段并合并零售指标。
// 拒绝返回部分数据：任一分段失败则整体失败，避免跨引擎口径不一致的半截汇总被当成正确结果。
// errgroup.WithContext 在首个错误时取消共享 ctx，使仍在阻塞的兄弟查询尽快收到 ctx.Done() 并退出。
func (s *QueryService) RetailSummary(ctx context.Context, requested domain.Source, q domain.Query) (RetailResult, error) {
	segments, err := s.Plan(requested, q.Range)
	if err != nil {
		return RetailResult{}, err
	}

	type part struct {
		seg  domain.Segment
		data domain.RetailSummary
	}
	parts := make([]part, len(segments))

	group, gctx := errgroup.WithContext(ctx)
	for i, seg := range segments {
		i, seg := i, seg
		group.Go(func() error {
			start := time.Now()
			repo, ok := s.repositories[seg.Source]
			if !ok || repo == nil {
				err := domain.ErrInvalidArgument
				s.observer.ObserveQuery(seg.Source, MetricRetail, time.Since(start), err)
				return err
			}
			data, err := repo.RetailSummary(gctx, domain.Query{Range: seg.Range, OrgID: q.OrgID})
			s.observer.ObserveQuery(seg.Source, MetricRetail, time.Since(start), err)
			if err != nil {
				return fmt.Errorf("retail %s: %w", seg.Source, err)
			}
			parts[i] = part{seg: seg, data: data}
			return nil
		})
	}

	// 仅在全部成功后再合并；失败路径直接返回零值，杜绝部分指标泄漏。
	if err := group.Wait(); err != nil {
		return RetailResult{}, err
	}

	var merged domain.RetailSummary
	merged.SalesAmount = decimal.Zero
	for _, p := range parts {
		merged.OrderCount, err = safeAddInt64(merged.OrderCount, p.data.OrderCount)
		if err != nil {
			return RetailResult{}, err
		}
		merged.SalesAmount = merged.SalesAmount.Add(p.data.SalesAmount)
	}
	return RetailResult{Data: merged, Segments: segments}, nil
}

// RenovationFunnel 并发查询各分段并合并家装漏斗计数。
// 同样拒绝部分成功：漏斗各阶段必须来自同一路由计划的完整分段集合。
func (s *QueryService) RenovationFunnel(ctx context.Context, requested domain.Source, q domain.Query) (RenovationResult, error) {
	segments, err := s.Plan(requested, q.Range)
	if err != nil {
		return RenovationResult{}, err
	}

	type part struct {
		seg  domain.Segment
		data domain.RenovationFunnel
	}
	parts := make([]part, len(segments))

	group, gctx := errgroup.WithContext(ctx)
	for i, seg := range segments {
		i, seg := i, seg
		group.Go(func() error {
			start := time.Now()
			repo, ok := s.repositories[seg.Source]
			if !ok || repo == nil {
				err := domain.ErrInvalidArgument
				s.observer.ObserveQuery(seg.Source, MetricRenovation, time.Since(start), err)
				return err
			}
			data, err := repo.RenovationFunnel(gctx, domain.Query{Range: seg.Range, OrgID: q.OrgID})
			s.observer.ObserveQuery(seg.Source, MetricRenovation, time.Since(start), err)
			if err != nil {
				return fmt.Errorf("renovation %s: %w", seg.Source, err)
			}
			parts[i] = part{seg: seg, data: data}
			return nil
		})
	}

	if err := group.Wait(); err != nil {
		return RenovationResult{}, err
	}

	var merged domain.RenovationFunnel
	add := func(dst *int64, v int64) error {
		n, err := safeAddInt64(*dst, v)
		if err != nil {
			return err
		}
		*dst = n
		return nil
	}
	for _, p := range parts {
		if err := add(&merged.LeadCount, p.data.LeadCount); err != nil {
			return RenovationResult{}, err
		}
		if err := add(&merged.InvitedCount, p.data.InvitedCount); err != nil {
			return RenovationResult{}, err
		}
		if err := add(&merged.MeasuredCount, p.data.MeasuredCount); err != nil {
			return RenovationResult{}, err
		}
		if err := add(&merged.SignedCount, p.data.SignedCount); err != nil {
			return RenovationResult{}, err
		}
		if err := add(&merged.StartedCount, p.data.StartedCount); err != nil {
			return RenovationResult{}, err
		}
		if err := add(&merged.CompletedCount, p.data.CompletedCount); err != nil {
			return RenovationResult{}, err
		}
	}
	return RenovationResult{Data: merged, Segments: segments}, nil
}

func safeAddInt64(a, b int64) (int64, error) {
	if b > 0 && a > math.MaxInt64-b {
		return 0, domain.ErrOverflow
	}
	if b < 0 && a < math.MinInt64-b {
		return 0, domain.ErrOverflow
	}
	return a + b, nil
}
