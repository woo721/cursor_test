package app_test

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/shopspring/decimal"

	"github.com/woo721/cursor_test/internal/app"
	"github.com/woo721/cursor_test/internal/domain"
)

type fakeClock struct{ now time.Time }

func (c fakeClock) Now() time.Time { return c.now }

type fakeRepository struct {
	retail           domain.RetailSummary
	funnel           domain.RenovationFunnel
	err              error
	called           chan domain.Query
	blockUntilCancel bool
	// waitBeforeReturn delays the error/success return until closed/signaled,
	// so a failing peer can wait until a blocking sibling has entered.
	waitBeforeReturn <-chan struct{}
	onBlock          func()
}

func (f *fakeRepository) RetailSummary(ctx context.Context, q domain.Query) (domain.RetailSummary, error) {
	if f.called != nil {
		f.called <- q
	}
	if f.waitBeforeReturn != nil {
		select {
		case <-f.waitBeforeReturn:
		case <-ctx.Done():
			return domain.RetailSummary{}, ctx.Err()
		}
	}
	if f.blockUntilCancel {
		if f.onBlock != nil {
			f.onBlock()
		}
		<-ctx.Done()
		return domain.RetailSummary{}, ctx.Err()
	}
	if f.err != nil {
		return domain.RetailSummary{}, f.err
	}
	return f.retail, nil
}

func (f *fakeRepository) RenovationFunnel(ctx context.Context, q domain.Query) (domain.RenovationFunnel, error) {
	if f.called != nil {
		f.called <- q
	}
	if f.waitBeforeReturn != nil {
		select {
		case <-f.waitBeforeReturn:
		case <-ctx.Done():
			return domain.RenovationFunnel{}, ctx.Err()
		}
	}
	if f.blockUntilCancel {
		if f.onBlock != nil {
			f.onBlock()
		}
		<-ctx.Done()
		return domain.RenovationFunnel{}, ctx.Err()
	}
	if f.err != nil {
		return domain.RenovationFunnel{}, f.err
	}
	return f.funnel, nil
}

func (f *fakeRepository) Ping(context.Context) error { return nil }

func mustLoc(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	return loc
}

func mustRange(t *testing.T, start, end string, loc *time.Location) domain.DateRange {
	t.Helper()
	r, err := domain.NewDateRange(start, end, 366, loc)
	if err != nil {
		t.Fatalf("NewDateRange(%s,%s): %v", start, end, err)
	}
	return r
}

func newTestService(
	t *testing.T,
	repos map[domain.Source]domain.Repository,
	historical domain.Source,
	realtimeDays int,
	loc *time.Location,
	now time.Time,
) *app.QueryService {
	t.Helper()
	svc, err := app.NewQueryService(repos, historical, realtimeDays, loc, fakeClock{now: now}, app.NopObserver{})
	if err != nil {
		t.Fatalf("NewQueryService: %v", err)
	}
	return svc
}

func TestPlanRoutesHistoryOnlyToConfiguredHive(t *testing.T) {
	loc := mustLoc(t)
	now := time.Date(2026, 8, 6, 15, 0, 0, 0, loc)
	hive := &fakeRepository{called: make(chan domain.Query, 1)}
	sr := &fakeRepository{called: make(chan domain.Query, 1)}
	svc := newTestService(t, map[domain.Source]domain.Repository{
		domain.SourceHive:      hive,
		domain.SourceStarRocks: sr,
	}, domain.SourceHive, 3, loc, now)

	r := mustRange(t, "2026-07-01", "2026-08-03", loc)
	got, err := svc.Plan(domain.SourceAuto, r)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	want := []domain.Segment{{Source: domain.SourceHive, Range: r}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("segments mismatch (-want +got):\n%s", diff)
	}
}

func TestPlanRoutesRealtimeOnlyToStarRocks(t *testing.T) {
	loc := mustLoc(t)
	now := time.Date(2026, 8, 6, 15, 0, 0, 0, loc)
	hive := &fakeRepository{}
	sr := &fakeRepository{}
	svc := newTestService(t, map[domain.Source]domain.Repository{
		domain.SourceHive:      hive,
		domain.SourceStarRocks: sr,
	}, domain.SourceHive, 3, loc, now)

	r := mustRange(t, "2026-08-04", "2026-08-06", loc)
	got, err := svc.Plan(domain.SourceAuto, r)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	want := []domain.Segment{{Source: domain.SourceStarRocks, Range: r}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("segments mismatch (-want +got):\n%s", diff)
	}
}

func TestRetailSummaryMergesCrossBoundarySegments(t *testing.T) {
	loc := mustLoc(t)
	now := time.Date(2026, 8, 6, 15, 0, 0, 0, loc)
	hive := &fakeRepository{
		retail: domain.RetailSummary{
			OrderCount:  10,
			SalesAmount: decimal.RequireFromString("20.00"),
		},
		called: make(chan domain.Query, 1),
	}
	sr := &fakeRepository{
		retail: domain.RetailSummary{
			OrderCount:  5,
			SalesAmount: decimal.RequireFromString("15.50"),
		},
		called: make(chan domain.Query, 1),
	}
	svc := newTestService(t, map[domain.Source]domain.Repository{
		domain.SourceHive:      hive,
		domain.SourceStarRocks: sr,
	}, domain.SourceHive, 3, loc, now)

	q := domain.Query{
		Range: mustRange(t, "2026-07-01", "2026-08-06", loc),
		OrgID: "ORG-001",
	}
	got, err := svc.RetailSummary(context.Background(), domain.SourceAuto, q)
	if err != nil {
		t.Fatalf("RetailSummary: %v", err)
	}
	if got.Data.OrderCount != 15 ||
		!got.Data.SalesAmount.Equal(decimal.RequireFromString("35.50")) {
		t.Fatalf("merged retail = %#v", got.Data)
	}
	wantSegments := []domain.Segment{
		{Source: domain.SourceHive, Range: mustRange(t, "2026-07-01", "2026-08-03", loc)},
		{Source: domain.SourceStarRocks, Range: mustRange(t, "2026-08-04", "2026-08-06", loc)},
	}
	if diff := cmp.Diff(wantSegments, got.Segments); diff != "" {
		t.Fatalf("segments mismatch (-want +got):\n%s", diff)
	}
}

func TestPlanExplicitTrinoUsesFullRange(t *testing.T) {
	loc := mustLoc(t)
	now := time.Date(2026, 8, 6, 15, 0, 0, 0, loc)
	trino := &fakeRepository{}
	hive := &fakeRepository{}
	sr := &fakeRepository{}
	svc := newTestService(t, map[domain.Source]domain.Repository{
		domain.SourceHive:      hive,
		domain.SourceTrino:     trino,
		domain.SourceStarRocks: sr,
	}, domain.SourceHive, 3, loc, now)

	r := mustRange(t, "2026-07-01", "2026-08-06", loc)
	got, err := svc.Plan(domain.SourceTrino, r)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	want := []domain.Segment{{Source: domain.SourceTrino, Range: r}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("segments mismatch (-want +got):\n%s", diff)
	}
}

func TestRetailSummaryCancelsPeerOnSegmentFailure(t *testing.T) {
	loc := mustLoc(t)
	now := time.Date(2026, 8, 6, 15, 0, 0, 0, loc)
	failErr := errors.New("hive down")
	peerBlocking := make(chan struct{})
	hive := &fakeRepository{
		err:              failErr,
		called:           make(chan domain.Query, 1),
		waitBeforeReturn: peerBlocking,
	}
	sr := &fakeRepository{
		retail: domain.RetailSummary{
			OrderCount:  999,
			SalesAmount: decimal.RequireFromString("999.99"),
		},
		called:           make(chan domain.Query, 1),
		blockUntilCancel: true,
		onBlock:          func() { close(peerBlocking) },
	}
	svc := newTestService(t, map[domain.Source]domain.Repository{
		domain.SourceHive:      hive,
		domain.SourceStarRocks: sr,
	}, domain.SourceHive, 3, loc, now)

	q := domain.Query{
		Range: mustRange(t, "2026-07-01", "2026-08-06", loc),
		OrgID: "ORG-001",
	}
	got, err := svc.RetailSummary(context.Background(), domain.SourceAuto, q)
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, failErr) {
		t.Fatalf("error = %v, want wrapped %v", err, failErr)
	}
	if got.Data.OrderCount != 0 || !got.Data.SalesAmount.IsZero() {
		t.Fatalf("partial metrics returned: %#v", got.Data)
	}
	select {
	case <-sr.called:
		// peer was started; errgroup cancelled it via ctx.Done()
	default:
		t.Fatal("expected StarRocks peer to be invoked")
	}
}

func TestRetailSummaryOverflowOnMaxInt64Counts(t *testing.T) {
	loc := mustLoc(t)
	now := time.Date(2026, 8, 6, 15, 0, 0, 0, loc)
	hive := &fakeRepository{
		retail: domain.RetailSummary{
			OrderCount:  math.MaxInt64,
			SalesAmount: decimal.Zero,
		},
	}
	sr := &fakeRepository{
		retail: domain.RetailSummary{
			OrderCount:  math.MaxInt64,
			SalesAmount: decimal.Zero,
		},
	}
	svc := newTestService(t, map[domain.Source]domain.Repository{
		domain.SourceHive:      hive,
		domain.SourceStarRocks: sr,
	}, domain.SourceHive, 3, loc, now)

	q := domain.Query{
		Range: mustRange(t, "2026-07-01", "2026-08-06", loc),
		OrgID: "ORG-001",
	}
	got, err := svc.RetailSummary(context.Background(), domain.SourceAuto, q)
	if !errors.Is(err, domain.ErrOverflow) {
		t.Fatalf("error = %v, want %v", err, domain.ErrOverflow)
	}
	if got.Data.OrderCount != 0 {
		t.Fatalf("partial metrics on overflow: %#v", got.Data)
	}
}

func TestNewQueryServiceRejectsInvalidConfig(t *testing.T) {
	loc := mustLoc(t)
	now := time.Date(2026, 8, 6, 15, 0, 0, 0, loc)
	repos := map[domain.Source]domain.Repository{
		domain.SourceHive:      &fakeRepository{},
		domain.SourceStarRocks: &fakeRepository{},
	}
	cases := []struct {
		name         string
		repos        map[domain.Source]domain.Repository
		historical   domain.Source
		realtimeDays int
		tz           *time.Location
		clock        app.Clock
		observer     app.QueryObserver
	}{
		{
			name:  "bad historical",
			repos: repos, historical: domain.SourceStarRocks, realtimeDays: 3,
			tz: loc, clock: fakeClock{now: now}, observer: app.NopObserver{},
		},
		{
			name:  "realtime < 1",
			repos: repos, historical: domain.SourceHive, realtimeDays: 0,
			tz: loc, clock: fakeClock{now: now}, observer: app.NopObserver{},
		},
		{
			name:       "missing starrocks",
			repos:      map[domain.Source]domain.Repository{domain.SourceHive: &fakeRepository{}},
			historical: domain.SourceHive, realtimeDays: 3,
			tz: loc, clock: fakeClock{now: now}, observer: app.NopObserver{},
		},
		{
			name:  "nil timezone",
			repos: repos, historical: domain.SourceHive, realtimeDays: 3,
			tz: nil, clock: fakeClock{now: now}, observer: app.NopObserver{},
		},
		{
			name:  "nil clock",
			repos: repos, historical: domain.SourceHive, realtimeDays: 3,
			tz: loc, clock: nil, observer: app.NopObserver{},
		},
		{
			name:  "nil observer",
			repos: repos, historical: domain.SourceHive, realtimeDays: 3,
			tz: loc, clock: fakeClock{now: now}, observer: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := app.NewQueryService(tc.repos, tc.historical, tc.realtimeDays, tc.tz, tc.clock, tc.observer)
			if !errors.Is(err, domain.ErrInvalidArgument) {
				t.Fatalf("error = %v, want %v", err, domain.ErrInvalidArgument)
			}
		})
	}
}
