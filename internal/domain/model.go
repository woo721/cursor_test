package domain

import (
	"context"
	"regexp"
	"time"

	"github.com/shopspring/decimal"
)

type Source string

const (
	SourceAuto      Source = "auto"
	SourceHive      Source = "hive"
	SourceTrino     Source = "trino"
	SourceStarRocks Source = "starrocks"
)

// ParseSource 将外部请求中的数据源标识解析为领域枚举。
// memory 不在此接受：它是运行时/测试用的内存模式，不属于对外公开的数据源契约，
// 避免客户端绕过路由与治理直接指定内部实现。
func ParseSource(s string) (Source, error) {
	switch s {
	case string(SourceAuto):
		return SourceAuto, nil
	case string(SourceHive):
		return SourceHive, nil
	case string(SourceTrino):
		return SourceTrino, nil
	case string(SourceStarRocks):
		return SourceStarRocks, nil
	default:
		return "", ErrInvalidArgument
	}
}

type DateRange struct{ Start, End time.Time }

type Query struct {
	Range DateRange
	OrgID string
}

type RetailSummary struct {
	OrderCount  int64
	SalesAmount decimal.Decimal
}

type RenovationFunnel struct {
	LeadCount, InvitedCount, MeasuredCount    int64
	SignedCount, StartedCount, CompletedCount int64
}

type Segment struct {
	Source Source
	Range  DateRange
}

type Repository interface {
	RetailSummary(context.Context, Query) (RetailSummary, error)
	RenovationFunnel(context.Context, Query) (RenovationFunnel, error)
	Ping(context.Context) error
}

var orgIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func ValidateOrgID(id string) error {
	if !orgIDPattern.MatchString(id) {
		return ErrInvalidArgument
	}
	return nil
}

// NewDateRange 在指定时区下解析起止自然日（YYYY-MM-DD），并按闭区间计数天数。
// 日期归一化到该时区午夜，是为了与业务报表的「自然日」口径一致，避免 UTC 与本地日界错位导致跨天统计偏差。
func NewDateRange(startStr, endStr string, maxDays int, loc *time.Location) (DateRange, error) {
	const layout = "2006-01-02"
	start, err := time.ParseInLocation(layout, startStr, loc)
	if err != nil {
		return DateRange{}, ErrInvalidArgument
	}
	end, err := time.ParseInLocation(layout, endStr, loc)
	if err != nil {
		return DateRange{}, ErrInvalidArgument
	}
	if end.Before(start) {
		return DateRange{}, ErrInvalidArgument
	}
	days := int(end.Sub(start)/(24*time.Hour)) + 1
	if days > maxDays {
		return DateRange{}, ErrInvalidArgument
	}
	return DateRange{Start: start, End: end}, nil
}

func (r DateRange) Days() int {
	return int(r.End.Sub(r.Start)/(24*time.Hour)) + 1
}

func (r DateRange) StartString() string {
	return r.Start.Format("2006-01-02")
}

func (r DateRange) EndString() string {
	return r.End.Format("2006-01-02")
}
