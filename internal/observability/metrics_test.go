package observability_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/woo721/cursor_test/internal/app"
	"github.com/woo721/cursor_test/internal/domain"
	"github.com/woo721/cursor_test/internal/observability"
)

func scrape(t *testing.T, h http.Handler) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics status=%d body=%s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func TestMetricsObserveQueryCounters(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := observability.NewMetrics(reg)

	metrics.ObserveQuery(domain.SourceHive, app.MetricRetail, 25*time.Millisecond, nil)
	metrics.ObserveQuery(domain.SourceHive, app.MetricRetail, 5*time.Millisecond, errors.New("boom"))

	body := scrape(t, promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	for _, want := range []string{
		`data_query_total{metric="retail",source="hive",status="success"} 1`,
		`data_query_total{metric="retail",source="hive",status="error"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q in:\n%s", want, body)
		}
	}
}

func TestMetricsMapsQueryErrorsToBoundedStatuses(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := observability.NewMetrics(reg)

	metrics.ObserveQuery(domain.SourceTrino, app.MetricRenovation, time.Millisecond, context.DeadlineExceeded)
	metrics.ObserveQuery(domain.SourceStarRocks, app.MetricRenovation, time.Millisecond, context.Canceled)

	body := scrape(t, promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	for _, want := range []string{
		`data_query_total{metric="renovation",source="trino",status="timeout"} 1`,
		`data_query_total{metric="renovation",source="starrocks",status="cancelled"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q in:\n%s", want, body)
		}
	}
}

func TestHTTPMetricsUseBoundedLabels(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := observability.NewMetrics(reg)
	handler := metrics.HTTPMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", "req-secret-123")
		w.WriteHeader(http.StatusAccepted)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/retail/summary?org_id=ORG-001&request_id=req-secret-123", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	body := scrape(t, metrics.Handler(reg))
	if !strings.Contains(body, `data_http_requests_total{method="GET",route="/api/v1/retail/summary",status="202"} 1`) {
		t.Fatalf("missing bounded HTTP counter in:\n%s", body)
	}
	for _, forbidden := range []string{"ORG-001", "req-secret-123", "org_id", "request_id"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("HTTP metrics leaked %q in:\n%s", forbidden, body)
		}
	}
}
