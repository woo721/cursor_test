package httpapi_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/woo721/cursor_test/internal/app"
	"github.com/woo721/cursor_test/internal/domain"
	"github.com/woo721/cursor_test/internal/httpapi"
)

type panicQueries struct{}

func (panicQueries) RetailSummary(context.Context, domain.Source, domain.Query) (app.RetailResult, error) {
	panic("secret stack detail")
}

func (panicQueries) RenovationFunnel(context.Context, domain.Source, domain.Query) (app.RenovationResult, error) {
	panic("secret stack detail")
}

func TestPanicRecovery(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(httpapi.New(httpapi.Dependencies{
		Queries:      panicQueries{},
		Ready:        fakeReady{},
		APIKey:       testAPIKey,
		QueryTimeout: time.Second,
		MaxQueryDays: 366,
		Timezone:     loc,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}))
	defer srv.Close()

	resp := authorizedGet(t, srv, "/api/v1/retail/summary?start_date=2026-08-01&end_date=2026-08-06&source=auto&org_id=ORG-001")
	if resp.StatusCode != http.StatusInternalServerError {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("status=%d body=%s", resp.StatusCode, body)
	}
	code, msg, rid := decodeError(t, resp)
	if code != "INTERNAL_ERROR" {
		t.Fatalf("code=%q", code)
	}
	if strings.Contains(msg, "secret") || strings.Contains(strings.ToLower(msg), "panic") {
		t.Fatalf("leaked panic text: %q", msg)
	}
	if rid == "" || rid != resp.Header.Get("X-Request-ID") {
		t.Fatalf("request_id mismatch")
	}
}

func TestRequestIDIsHex32(t *testing.T) {
	srv := testServer(t, &fakeQueries{}, nil)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health/live")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	id := resp.Header.Get("X-Request-ID")
	if len(id) != 32 {
		t.Fatalf("request id length=%d want 32 hex chars from 16 bytes", len(id))
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			t.Fatalf("non-lowercase-hex char in request id: %q", id)
		}
	}
}
