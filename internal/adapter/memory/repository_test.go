package memory_test

import (
	"context"
	"hash/fnv"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/shopspring/decimal"

	"github.com/woo721/cursor_test/internal/adapter/memory"
	"github.com/woo721/cursor_test/internal/domain"
)

func stableOrgSeed(orgID string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(orgID))
	return h.Sum64()
}

func dayNumberUTC(t time.Time) int64 {
	y, m, d := t.Date()
	utc := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	return utc.Unix() / (24 * 60 * 60)
}

func dailyRetail(orgID string, day time.Time) domain.RetailSummary {
	seed := stableOrgSeed(orgID)
	dn := dayNumberUTC(day)
	orders := int64(60 + (uint64(dn)+seed)%41)
	priceCents := int64(129900 + (seed % 70000))
	salesCents := orders * priceCents
	return domain.RetailSummary{
		OrderCount:  orders,
		SalesAmount: decimal.NewFromInt(salesCents).Shift(-2),
	}
}

func dailyRenovation(orgID string, day time.Time) domain.RenovationFunnel {
	seed := stableOrgSeed(orgID)
	dn := dayNumberUTC(day)
	offset := int64((uint64(dn) + seed) % 41)
	leads := int64(35 + offset)
	invited := leads * 72 / 100
	measured := invited * 64 / 100
	signed := measured * 46 / 100
	started := signed * 78 / 100
	completed := started * 74 / 100
	return domain.RenovationFunnel{
		LeadCount:      leads,
		InvitedCount:   invited,
		MeasuredCount:  measured,
		SignedCount:    signed,
		StartedCount:   started,
		CompletedCount: completed,
	}
}

func sumRetail(days ...time.Time) domain.RetailSummary {
	var total domain.RetailSummary
	for _, d := range days {
		daily := dailyRetail("ORG-001", d)
		total.OrderCount += daily.OrderCount
		total.SalesAmount = total.SalesAmount.Add(daily.SalesAmount)
	}
	return total
}

func sumRenovation(orgID string, days ...time.Time) domain.RenovationFunnel {
	var total domain.RenovationFunnel
	for _, d := range days {
		daily := dailyRenovation(orgID, d)
		total.LeadCount += daily.LeadCount
		total.InvitedCount += daily.InvitedCount
		total.MeasuredCount += daily.MeasuredCount
		total.SignedCount += daily.SignedCount
		total.StartedCount += daily.StartedCount
		total.CompletedCount += daily.CompletedCount
	}
	return total
}

func mustRange(t *testing.T, start, end string) domain.DateRange {
	t.Helper()
	r, err := domain.NewDateRange(start, end, 366, time.UTC)
	if err != nil {
		t.Fatalf("NewDateRange: %v", err)
	}
	return r
}

func TestRetailSummaryIsDeterministic(t *testing.T) {
	repo := memory.New()
	q := domain.Query{
		OrgID: "ORG-001",
		Range: mustRange(t, "2026-08-01", "2026-08-06"),
	}
	ctx := context.Background()

	first, err := repo.RetailSummary(ctx, q)
	if err != nil {
		t.Fatalf("RetailSummary: %v", err)
	}
	second, err := repo.RetailSummary(ctx, q)
	if err != nil {
		t.Fatalf("RetailSummary: %v", err)
	}
	if diff := cmp.Diff(first, second); diff != "" {
		t.Fatalf("deterministic retail mismatch (-first +second):\n%s", diff)
	}

	want := sumRetail(
		time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC),
	)
	if diff := cmp.Diff(want, first); diff != "" {
		t.Fatalf("retail formula mismatch (-want +got):\n%s", diff)
	}
}

func TestRetailSummaryRangeExtensionAddsOneDay(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()
	shorter := domain.Query{
		OrgID: "ORG-001",
		Range: mustRange(t, "2026-08-01", "2026-08-05"),
	}
	extended := domain.Query{
		OrgID: "ORG-001",
		Range: mustRange(t, "2026-08-01", "2026-08-06"),
	}
	extraDay := domain.Query{
		OrgID: "ORG-001",
		Range: mustRange(t, "2026-08-06", "2026-08-06"),
	}

	shortSum, err := repo.RetailSummary(ctx, shorter)
	if err != nil {
		t.Fatalf("RetailSummary shorter: %v", err)
	}
	oneDay, err := repo.RetailSummary(ctx, extraDay)
	if err != nil {
		t.Fatalf("RetailSummary one day: %v", err)
	}
	full, err := repo.RetailSummary(ctx, extended)
	if err != nil {
		t.Fatalf("RetailSummary extended: %v", err)
	}

	want := domain.RetailSummary{
		OrderCount:  shortSum.OrderCount + oneDay.OrderCount,
		SalesAmount: shortSum.SalesAmount.Add(oneDay.SalesAmount),
	}
	if diff := cmp.Diff(want, full); diff != "" {
		t.Fatalf("range extension mismatch (-want +got):\n%s", diff)
	}
}

func TestDistinctOrganizationsProduceDistinctRetail(t *testing.T) {
	repo := memory.New()
	ctx := context.Background()
	rng := mustRange(t, "2026-08-01", "2026-08-03")
	a, err := repo.RetailSummary(ctx, domain.Query{OrgID: "ORG-001", Range: rng})
	if err != nil {
		t.Fatalf("ORG-001: %v", err)
	}
	b, err := repo.RetailSummary(ctx, domain.Query{OrgID: "ORG-002", Range: rng})
	if err != nil {
		t.Fatalf("ORG-002: %v", err)
	}
	if a.OrderCount == b.OrderCount && a.SalesAmount.Equal(b.SalesAmount) {
		t.Fatalf("expected distinct retail metrics, both got %#v", a)
	}
}

func TestRetailSalesAmountHasTwoDecimalPlaces(t *testing.T) {
	repo := memory.New()
	got, err := repo.RetailSummary(context.Background(), domain.Query{
		OrgID: "ORG-001",
		Range: mustRange(t, "2026-08-01", "2026-08-31"),
	})
	if err != nil {
		t.Fatalf("RetailSummary: %v", err)
	}
	if got.SalesAmount.Exponent() != -2 {
		t.Fatalf("sales amount exponent = %d, want -2 (%s)", got.SalesAmount.Exponent(), got.SalesAmount)
	}
}

func TestRenovationFunnelMatchesDailyFormula(t *testing.T) {
	repo := memory.New()
	q := domain.Query{
		OrgID: "ORG-001",
		Range: mustRange(t, "2026-08-01", "2026-08-06"),
	}
	got, err := repo.RenovationFunnel(context.Background(), q)
	if err != nil {
		t.Fatalf("RenovationFunnel: %v", err)
	}
	want := sumRenovation("ORG-001",
		time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 4, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC),
	)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("renovation formula mismatch (-want +got):\n%s", diff)
	}
}

func TestPingSucceedsUnlessContextCancelled(t *testing.T) {
	repo := memory.New()
	if err := repo.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := repo.Ping(ctx); err == nil {
		t.Fatal("Ping with cancelled context: want error")
	}
}

func TestRetailSummaryRespectsContextCancellation(t *testing.T) {
	repo := memory.New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := repo.RetailSummary(ctx, domain.Query{
		OrgID: "ORG-001",
		Range: mustRange(t, "2026-08-01", "2026-08-06"),
	})
	if err == nil {
		t.Fatal("RetailSummary: want cancellation error")
	}
}
