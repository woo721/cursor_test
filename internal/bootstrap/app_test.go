package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/woo721/cursor_test/internal/config"
	"github.com/woo721/cursor_test/internal/domain"
)

type fakeDB struct {
	name        string
	pingErr     error
	closeErr    error
	pingStarted func()
	pingRelease <-chan struct{}

	mu        sync.Mutex
	pings     int
	closes    int
	deadlined bool
}

func (f *fakeDB) QueryRowContext(context.Context, string, ...any) *sql.Row { return nil }

func (f *fakeDB) PingContext(ctx context.Context) error {
	if _, ok := ctx.Deadline(); ok {
		f.mu.Lock()
		f.deadlined = true
		f.mu.Unlock()
	}
	if f.pingStarted != nil {
		f.pingStarted()
	}
	if f.pingRelease != nil {
		select {
		case <-f.pingRelease:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	f.pings++
	f.mu.Unlock()
	return f.pingErr
}

func (f *fakeDB) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closes++
	return f.closeErr
}

func (f *fakeDB) counts() (pings, closes int, deadlined bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pings, f.closes, f.deadlined
}

func testConfig(mode string) config.Config {
	return config.Config{
		Server: config.Server{
			Addr:            "127.0.0.1:0",
			Timezone:        time.UTC,
			ShutdownTimeout: 100 * time.Millisecond,
		},
		Query: config.Query{
			Timeout:            time.Second,
			MaxQueryDays:       366,
			RealtimeWindowDays: 3,
			HistoricalSource:   "hive",
		},
		DataMode: mode,
		APIKey:   "test-api-key",
		Tables: config.Tables{
			Prefix:     "analytics",
			Retail:     "retail",
			Renovation: "renovation",
		},
	}
}

func fakeBuilders(dbs map[domain.Source]*fakeDB) builders {
	return builders{
		openHive: func(config.Hive) (ownedDB, error) {
			return dbs[domain.SourceHive], nil
		},
		openTrino: func(config.Trino) (ownedDB, error) {
			return dbs[domain.SourceTrino], nil
		},
		openStarRocks: func(config.StarRocks) (ownedDB, error) {
			return dbs[domain.SourceStarRocks], nil
		},
		startupTimeout: 100 * time.Millisecond,
	}
}

func TestBuildMemoryModeUsesDeterministicRepositoriesWithoutNetwork(t *testing.T) {
	var opened int
	b := builders{
		openHive: func(config.Hive) (ownedDB, error) {
			opened++
			return nil, errors.New("network should not be used")
		},
		openTrino: func(config.Trino) (ownedDB, error) {
			opened++
			return nil, errors.New("network should not be used")
		},
		openStarRocks: func(config.StarRocks) (ownedDB, error) {
			opened++
			return nil, errors.New("network should not be used")
		},
	}

	application, err := buildWith(testConfig("memory"), b)
	if err != nil {
		t.Fatalf("Build memory: %v", err)
	}
	defer application.Close()
	if opened != 0 {
		t.Fatalf("memory mode opened %d network connections", opened)
	}

	for _, source := range []domain.Source{domain.SourceHive, domain.SourceTrino, domain.SourceStarRocks} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/retail/summary?start_date=2026-08-01&end_date=2026-08-01&source="+string(source)+"&org_id=ORG-001", nil)
		req.Header.Set("X-API-Key", "test-api-key")
		rec := httptest.NewRecorder()
		application.Handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", source, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"order_count":`) {
			t.Fatalf("%s missing deterministic memory response: %s", source, rec.Body.String())
		}
	}
}

func TestBuildRealModePingsConcurrentlyAndClosesOnStartupFailure(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 3)
	var once sync.Once
	dbs := map[domain.Source]*fakeDB{
		domain.SourceHive:      {name: "hive", pingRelease: release, pingStarted: func() { started <- struct{}{} }},
		domain.SourceTrino:     {name: "trino", pingErr: errors.New("trino down"), pingRelease: release, pingStarted: func() { started <- struct{}{} }},
		domain.SourceStarRocks: {name: "starrocks", pingRelease: release, pingStarted: func() { started <- struct{}{} }},
	}

	done := make(chan error, 1)
	go func() {
		_, err := buildWith(testConfig("real"), fakeBuilders(dbs))
		done <- err
	}()

	for i := 0; i < 3; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("startup pings were not concurrent")
		}
	}
	once.Do(func() { close(release) })

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected startup ping error")
		}
	case <-time.After(time.Second):
		t.Fatal("Build did not return")
	}

	for source, db := range dbs {
		_, closes, deadlined := db.counts()
		if closes != 1 {
			t.Fatalf("%s closes=%d want 1", source, closes)
		}
		if !deadlined {
			t.Fatalf("%s ping did not receive startup deadline", source)
		}
	}
}

func TestReadinessPingsEachUniqueActiveRepository(t *testing.T) {
	dbs := map[domain.Source]*fakeDB{
		domain.SourceHive:      {name: "hive"},
		domain.SourceTrino:     {name: "trino"},
		domain.SourceStarRocks: {name: "starrocks"},
	}
	application, err := buildWith(testConfig("real"), fakeBuilders(dbs))
	if err != nil {
		t.Fatalf("Build real: %v", err)
	}
	defer application.Close()
	for _, db := range dbs {
		db.mu.Lock()
		db.pings = 0
		db.mu.Unlock()
	}

	req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	rec := httptest.NewRecorder()
	application.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ready status=%d body=%s", rec.Code, rec.Body.String())
	}
	for source, db := range dbs {
		pings, _, _ := db.counts()
		if pings != 1 {
			t.Fatalf("%s readiness pings=%d want 1", source, pings)
		}
	}
}

func TestApplicationCloseIsIdempotent(t *testing.T) {
	first := &fakeDB{name: "first"}
	second := &fakeDB{name: "second", closeErr: errors.New("close failed")}
	application := &Application{closers: []io.Closer{first, second}}

	err1 := application.Close()
	err2 := application.Close()
	if err1 == nil || err2 == nil {
		t.Fatalf("expected close error, got err1=%v err2=%v", err1, err2)
	}
	if err1 != err2 {
		t.Fatalf("Close should return the cached error on later calls")
	}
	for _, db := range []*fakeDB{first, second} {
		_, closes, _ := db.counts()
		if closes != 1 {
			t.Fatalf("%s closes=%d want 1", db.name, closes)
		}
	}
}
