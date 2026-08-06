package memory

import (
	"context"
	"hash/fnv"
	"time"

	"github.com/shopspring/decimal"

	"github.com/woo721/cursor_test/internal/domain"
)

// Repository 以确定性公式生成本地演示数据，便于 DATA_MODE=memory 下无需外部集群即可跑通 API。
type Repository struct{}

func New() *Repository { return &Repository{} }

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

func eachDay(r domain.DateRange, fn func(time.Time) error) error {
	loc := r.Start.Location()
	start := time.Date(r.Start.Year(), r.Start.Month(), r.Start.Day(), 0, 0, 0, 0, loc)
	end := time.Date(r.End.Year(), r.End.Month(), r.End.Day(), 0, 0, 0, 0, loc)
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		if err := fn(d); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) RetailSummary(ctx context.Context, q domain.Query) (domain.RetailSummary, error) {
	var total domain.RetailSummary
	seed := stableOrgSeed(q.OrgID)
	err := eachDay(q.Range, func(day time.Time) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		dn := dayNumberUTC(day)
		orders := int64(60 + (uint64(dn)+seed)%41)
		priceCents := int64(129900 + (seed % 70000))
		salesCents := orders * priceCents
		total.OrderCount += orders
		amount := decimal.NewFromInt(salesCents).Shift(-2)
		total.SalesAmount = total.SalesAmount.Add(amount)
		return nil
	})
	if err != nil {
		return domain.RetailSummary{}, err
	}
	return total, nil
}

func (r *Repository) RenovationFunnel(ctx context.Context, q domain.Query) (domain.RenovationFunnel, error) {
	var total domain.RenovationFunnel
	seed := stableOrgSeed(q.OrgID)
	err := eachDay(q.Range, func(day time.Time) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		dn := dayNumberUTC(day)
		offset := int64((uint64(dn) + seed) % 41)
		leads := int64(35 + offset)
		invited := leads * 72 / 100
		measured := invited * 64 / 100
		signed := measured * 46 / 100
		started := signed * 78 / 100
		completed := started * 74 / 100
		total.LeadCount += leads
		total.InvitedCount += invited
		total.MeasuredCount += measured
		total.SignedCount += signed
		total.StartedCount += started
		total.CompletedCount += completed
		return nil
	})
	if err != nil {
		return domain.RenovationFunnel{}, err
	}
	return total, nil
}

func (r *Repository) Ping(ctx context.Context) error {
	return ctx.Err()
}
