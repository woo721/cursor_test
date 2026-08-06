package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/woo721/cursor_test/internal/app"
	"github.com/woo721/cursor_test/internal/domain"
	"github.com/woo721/cursor_test/internal/httpapi"
)

const testAPIKey = "test-secret-key"

type fakeQueries struct {
	retail     app.RetailResult
	renovation app.RenovationResult
	err        error
	lastSource domain.Source
	lastQuery  domain.Query
}

func (f *fakeQueries) RetailSummary(ctx context.Context, source domain.Source, query domain.Query) (app.RetailResult, error) {
	f.lastSource = source
	f.lastQuery = query
	if f.err != nil {
		return app.RetailResult{}, f.err
	}
	return f.retail, nil
}

func (f *fakeQueries) RenovationFunnel(ctx context.Context, source domain.Source, query domain.Query) (app.RenovationResult, error) {
	f.lastSource = source
	f.lastQuery = query
	if f.err != nil {
		return app.RenovationResult{}, f.err
	}
	return f.renovation, nil
}

type fakeReady struct {
	err error
}

func (f fakeReady) Ping(context.Context) error { return f.err }

func testServer(t *testing.T, q *fakeQueries, ready httpapi.Readiness) *httptest.Server {
	t.Helper()
	if ready == nil {
		ready = fakeReady{}
	}
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	h := httpapi.New(httpapi.Dependencies{
		Queries:      q,
		Ready:        ready,
		APIKey:       testAPIKey,
		QueryTimeout: 2 * time.Second,
		MaxQueryDays: 366,
		Timezone:     loc,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return httptest.NewServer(h)
}

func authorizedGet(t *testing.T, server *httptest.Server, path string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-API-Key", testAPIKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func decodeError(t *testing.T, resp *http.Response) (code, message, requestID string) {
	t.Helper()
	defer resp.Body.Close()
	var body struct {
		Error struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			RequestID string `json:"request_id"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return body.Error.Code, body.Error.Message, body.Error.RequestID
}

func TestMissingAPIKeyUnauthorized(t *testing.T) {
	srv := testServer(t, &fakeQueries{}, nil)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/retail/summary?start_date=2026-08-01&end_date=2026-08-06&source=auto&org_id=ORG-001")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", resp.StatusCode)
	}
	if resp.Header.Get("WWW-Authenticate") != "" {
		t.Fatalf("WWW-Authenticate must be empty, got %q", resp.Header.Get("WWW-Authenticate"))
	}
	code, _, rid := decodeError(t, resp)
	if code != "UNAUTHORIZED" {
		t.Fatalf("code=%q", code)
	}
	if rid == "" || rid != resp.Header.Get("X-Request-ID") {
		t.Fatalf("request_id mismatch body=%q header=%q", rid, resp.Header.Get("X-Request-ID"))
	}
}

func TestWrongAPIKeyUnauthorized(t *testing.T) {
	srv := testServer(t, &fakeQueries{}, nil)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/retail/summary?start_date=2026-08-01&end_date=2026-08-06&source=auto&org_id=ORG-001", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-API-Key", "wrong-key")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", resp.StatusCode)
	}
	if resp.Header.Get("WWW-Authenticate") != "" {
		t.Fatalf("WWW-Authenticate must be empty")
	}
	code, _, _ := decodeError(t, resp)
	if code != "UNAUTHORIZED" {
		t.Fatalf("code=%q", code)
	}
}

func TestRetailSummarySuccess(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, loc)
	mid := time.Date(2026, 8, 3, 0, 0, 0, 0, loc)
	end := time.Date(2026, 8, 6, 0, 0, 0, 0, loc)
	q := &fakeQueries{
		retail: app.RetailResult{
			Data: domain.RetailSummary{
				OrderCount:  1280,
				SalesAmount: decimal.RequireFromString("3568200.50"),
			},
			Segments: []domain.Segment{
				{Source: domain.SourceHive, Range: domain.DateRange{Start: start, End: mid}},
				{Source: domain.SourceStarRocks, Range: domain.DateRange{Start: mid.AddDate(0, 0, 1), End: end}},
			},
		},
	}
	srv := testServer(t, q, nil)
	defer srv.Close()

	resp := authorizedGet(t, srv, "/api/v1/retail/summary?start_date=2026-08-01&end_date=2026-08-06&source=auto&org_id=ORG-001")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if resp.Header.Get("X-Request-ID") == "" {
		t.Fatal("missing X-Request-ID")
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("content-type=%q", ct)
	}

	var body struct {
		Data struct {
			OrderCount  int64  `json:"order_count"`
			SalesAmount string `json:"sales_amount"`
		} `json:"data"`
		Meta struct {
			RequestID       string `json:"request_id"`
			RequestedSource string `json:"requested_source"`
			Segments        []struct {
				Source    string `json:"source"`
				StartDate string `json:"start_date"`
				EndDate   string `json:"end_date"`
			} `json:"segments"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Data.OrderCount != 1280 {
		t.Fatalf("order_count=%d", body.Data.OrderCount)
	}
	if body.Data.SalesAmount != "3568200.50" {
		t.Fatalf("sales_amount=%q want 3568200.50", body.Data.SalesAmount)
	}
	if body.Meta.RequestedSource != "auto" {
		t.Fatalf("requested_source=%q", body.Meta.RequestedSource)
	}
	if body.Meta.RequestID != resp.Header.Get("X-Request-ID") {
		t.Fatalf("meta.request_id mismatch")
	}
	if len(body.Meta.Segments) != 2 {
		t.Fatalf("segments=%d", len(body.Meta.Segments))
	}
	if body.Meta.Segments[0].Source != "hive" || body.Meta.Segments[0].StartDate != "2026-08-01" || body.Meta.Segments[0].EndDate != "2026-08-03" {
		t.Fatalf("segment0=%+v", body.Meta.Segments[0])
	}
	if body.Meta.Segments[1].Source != "starrocks" || body.Meta.Segments[1].StartDate != "2026-08-04" || body.Meta.Segments[1].EndDate != "2026-08-06" {
		t.Fatalf("segment1=%+v", body.Meta.Segments[1])
	}
	if q.lastSource != domain.SourceAuto || q.lastQuery.OrgID != "ORG-001" {
		t.Fatalf("service args source=%s org=%s", q.lastSource, q.lastQuery.OrgID)
	}
}

func TestRenovationFunnelSuccess(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, loc)
	end := time.Date(2026, 8, 6, 0, 0, 0, 0, loc)
	q := &fakeQueries{
		renovation: app.RenovationResult{
			Data: domain.RenovationFunnel{
				LeadCount: 1000, InvitedCount: 720, MeasuredCount: 460,
				SignedCount: 210, StartedCount: 160, CompletedCount: 120,
			},
			Segments: []domain.Segment{
				{Source: domain.SourceStarRocks, Range: domain.DateRange{Start: start, End: end}},
			},
		},
	}
	srv := testServer(t, q, nil)
	defer srv.Close()

	resp := authorizedGet(t, srv, "/api/v1/renovation/funnel?start_date=2026-08-01&end_date=2026-08-06&source=starrocks&org_id=ORG-001")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var body struct {
		Data struct {
			LeadCount      int64 `json:"lead_count"`
			InvitedCount   int64 `json:"invited_count"`
			MeasuredCount  int64 `json:"measured_count"`
			SignedCount    int64 `json:"signed_count"`
			StartedCount   int64 `json:"started_count"`
			CompletedCount int64 `json:"completed_count"`
		} `json:"data"`
		Meta struct {
			RequestedSource string `json:"requested_source"`
		} `json:"meta"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Data.LeadCount != 1000 || body.Data.CompletedCount != 120 {
		t.Fatalf("data=%+v", body.Data)
	}
	if body.Meta.RequestedSource != "starrocks" {
		t.Fatalf("requested_source=%q", body.Meta.RequestedSource)
	}
}

func TestInvalidQueryParams(t *testing.T) {
	srv := testServer(t, &fakeQueries{}, nil)
	defer srv.Close()

	cases := []struct {
		name string
		path string
	}{
		{"bad date", "/api/v1/retail/summary?start_date=not-a-date&end_date=2026-08-06&source=auto&org_id=ORG-001"},
		{"bad source", "/api/v1/retail/summary?start_date=2026-08-01&end_date=2026-08-06&source=memory&org_id=ORG-001"},
		{"bad org", "/api/v1/retail/summary?start_date=2026-08-01&end_date=2026-08-06&source=auto&org_id=bad%20org"},
		{"span too long", "/api/v1/retail/summary?start_date=2025-01-01&end_date=2026-08-06&source=auto&org_id=ORG-001"},
		{"end before start", "/api/v1/retail/summary?start_date=2026-08-06&end_date=2026-08-01&source=auto&org_id=ORG-001"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := authorizedGet(t, srv, tc.path)
			if resp.StatusCode != http.StatusBadRequest {
				resp.Body.Close()
				t.Fatalf("status=%d want 400", resp.StatusCode)
			}
			code, _, rid := decodeError(t, resp)
			if code != "INVALID_ARGUMENT" {
				t.Fatalf("code=%q", code)
			}
			if rid == "" || rid != resp.Header.Get("X-Request-ID") {
				t.Fatalf("request_id mismatch")
			}
		})
	}
}

func TestDependencyUnavailable(t *testing.T) {
	q := &fakeQueries{err: domain.ErrDependencyUnavailable}
	srv := testServer(t, q, nil)
	defer srv.Close()

	resp := authorizedGet(t, srv, "/api/v1/retail/summary?start_date=2026-08-01&end_date=2026-08-06&source=auto&org_id=ORG-001")
	if resp.StatusCode != http.StatusServiceUnavailable {
		resp.Body.Close()
		t.Fatalf("status=%d want 503", resp.StatusCode)
	}
	code, _, _ := decodeError(t, resp)
	if code != "DEPENDENCY_UNAVAILABLE" {
		t.Fatalf("code=%q", code)
	}
}

func TestQueryTimeout(t *testing.T) {
	q := &fakeQueries{err: context.DeadlineExceeded}
	srv := testServer(t, q, nil)
	defer srv.Close()

	resp := authorizedGet(t, srv, "/api/v1/retail/summary?start_date=2026-08-01&end_date=2026-08-06&source=auto&org_id=ORG-001")
	if resp.StatusCode != http.StatusGatewayTimeout {
		resp.Body.Close()
		t.Fatalf("status=%d want 504", resp.StatusCode)
	}
	code, msg, _ := decodeError(t, resp)
	if code != "QUERY_TIMEOUT" {
		t.Fatalf("code=%q", code)
	}
	if msg != "query timed out" {
		t.Fatalf("message=%q", msg)
	}
}

func TestInternalError(t *testing.T) {
	q := &fakeQueries{err: errors.New("boom driver detail")}
	srv := testServer(t, q, nil)
	defer srv.Close()

	resp := authorizedGet(t, srv, "/api/v1/retail/summary?start_date=2026-08-01&end_date=2026-08-06&source=auto&org_id=ORG-001")
	if resp.StatusCode != http.StatusInternalServerError {
		resp.Body.Close()
		t.Fatalf("status=%d want 500", resp.StatusCode)
	}
	code, msg, _ := decodeError(t, resp)
	if code != "INTERNAL_ERROR" {
		t.Fatalf("code=%q", code)
	}
	if strings.Contains(msg, "boom") || strings.Contains(msg, "driver") {
		t.Fatalf("leaked internal detail: %q", msg)
	}
}

func TestPublicEndpointsSkipAPIKey(t *testing.T) {
	ready := fakeReady{}
	srv := testServer(t, &fakeQueries{}, ready)
	defer srv.Close()

	for _, path := range []string{"/health/live", "/health/ready", "/metrics"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, resp.StatusCode, body)
		}
		if resp.Header.Get("X-Request-ID") == "" {
			t.Fatalf("%s missing X-Request-ID", path)
		}
	}
}

func TestReadyFailsWhenPingFails(t *testing.T) {
	srv := testServer(t, &fakeQueries{}, fakeReady{err: domain.ErrDependencyUnavailable})
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health/ready")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", resp.StatusCode)
	}
}
