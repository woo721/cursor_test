package app

import (
	"time"

	"github.com/woo721/cursor_test/internal/domain"
)

type Metric string

const (
	MetricRetail     Metric = "retail"
	MetricRenovation Metric = "renovation"
)

type QueryObserver interface {
	ObserveQuery(source domain.Source, metric Metric, elapsed time.Duration, err error)
}

type NopObserver struct{}

func (NopObserver) ObserveQuery(domain.Source, Metric, time.Duration, error) {}
