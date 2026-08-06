package observability

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/woo721/cursor_test/internal/app"
	"github.com/woo721/cursor_test/internal/domain"
)

type Metrics struct {
	queryTotal       *prometheus.CounterVec
	queryDuration    *prometheus.HistogramVec
	httpTotal        *prometheus.CounterVec
	httpDuration     *prometheus.HistogramVec
	inFlightRequests prometheus.Gauge
}

func NewMetrics(reg prometheus.Registerer) *Metrics {
	if reg == nil {
		reg = prometheus.NewRegistry()
	}
	m := &Metrics{
		queryTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "data_query_total",
			Help: "Total data queries by source, metric, and bounded outcome.",
		}, []string{"source", "metric", "status"}),
		queryDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "data_query_duration_seconds",
			Help:    "Data query latency by source, metric, and bounded outcome.",
			Buckets: prometheus.DefBuckets,
		}, []string{"source", "metric", "status"}),
		httpTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "data_http_requests_total",
			Help: "Total HTTP requests by method, route, and status.",
		}, []string{"method", "route", "status"}),
		httpDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "data_http_request_duration_seconds",
			Help:    "HTTP request latency by method, route, and status.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "route", "status"}),
		inFlightRequests: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "data_http_in_flight_requests",
			Help: "Current in-flight HTTP requests.",
		}),
	}
	reg.MustRegister(m.queryTotal, m.queryDuration, m.httpTotal, m.httpDuration, m.inFlightRequests)
	return m
}

func (m *Metrics) ObserveQuery(source domain.Source, metric app.Metric, elapsed time.Duration, err error) {
	status := queryStatus(err)
	labels := prometheus.Labels{
		"source": string(source),
		"metric": string(metric),
		"status": status,
	}
	m.queryTotal.With(labels).Inc()
	m.queryDuration.With(labels).Observe(elapsed.Seconds())
}

func (m *Metrics) Handler(g prometheus.Gatherer) http.Handler {
	if g == nil {
		g = prometheus.DefaultGatherer
	}
	return promhttp.HandlerFor(g, promhttp.HandlerOpts{})
}

func (m *Metrics) HTTPMiddleware(next http.Handler) http.Handler {
	if next == nil {
		next = http.NotFoundHandler()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		m.inFlightRequests.Inc()
		defer m.inFlightRequests.Dec()

		next.ServeHTTP(sw, r)

		labels := prometheus.Labels{
			"method": r.Method,
			"route":  routeLabel(r),
			"status": strconv.Itoa(sw.status),
		}
		m.httpTotal.With(labels).Inc()
		m.httpDuration.With(labels).Observe(time.Since(start).Seconds())
	})
}

func queryStatus(err error) string {
	switch {
	case err == nil:
		return "success"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	default:
		return "error"
	}
}

func routeLabel(r *http.Request) string {
	if r.Pattern != "" {
		return r.Pattern
	}
	// 指标标签只允许低基数路由模板，不采集 org_id/request_id 等高基数请求数据。
	switch r.URL.Path {
	case "/api/v1/retail/summary",
		"/api/v1/renovation/funnel",
		"/health/live",
		"/health/ready",
		"/metrics":
		return r.URL.Path
	default:
		return "unknown"
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}
