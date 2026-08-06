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

// stableOrgSeed 用 FNV-1a 将 org_id 映射为固定 uint64，作为该租户在全部日级公式中的盐值。
// 同一 org 跨进程、跨重启结果一致；不同 org 在模运算下分布不同，便于联调时区分租户曲线。
func stableOrgSeed(orgID string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(orgID))
	return h.Sum64()
}

// dayNumberUTC 把日历日转为 UTC 午夜起的连续日序号（Unix 秒 / 86400），与查询范围的 Location 无关。
// eachDay 传入的是范围起止所在地的「自然日」零点；此处只取 Year/Month/Day 再归一到 UTC 零点，
// 保证同一公历日在全球租户下得到同一 dn，日级波动只随日历推进而非随客户端时区漂移。
func dayNumberUTC(t time.Time) int64 {
	y, m, d := t.Date()
	utc := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	return utc.Unix() / (24 * 60 * 60)
}

// eachDay 在 DateRange 的起止 Location 下按 inclusive 自然日逐步回调，与 API 层「按当地日历选区间」语义一致。
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
		// 聚合按日推进：任一自然日处理前检查 ctx，长区间或客户端取消时可尽快退出而非算完全部天数。
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
		// offset 与零售 orders 项共用 (dn+seed)%41，仅常数项不同（零售 +60 / 线索 +35），
		// 使同一 org、同一自然日的「日强度」在两条业务线上对齐，演示数据更易对照。
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
