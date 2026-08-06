# Go Home Improvement Query API Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a production-grade, heavily documented Go API that queries home-improvement retail and renovation-stage metrics from HiveServer2, Trino, and StarRocks, with a no-dependency memory demo mode.

**Architecture:** A modular monolith exposes business-oriented REST endpoints. An application service plans historical and real-time query segments, executes cross-engine segments concurrently, and merges exact results through a repository port. Memory and SQL adapters implement that port; HTTP, observability, configuration, and lifecycle code remain outside the domain.

**Tech Stack:** Go 1.26.5, `net/http`, `database/sql`, gohive v2, official Trino Go client, Go MySQL driver, `shopspring/decimal`, `errgroup`, Prometheus client, `sqlmock`.

## Global Constraints

- Follow test-driven development: add one failing behavior test, observe the expected failure, implement the minimum behavior, and rerun.
- Use Go 1.26.5; allow the installed Go 1.22 launcher to download the declared toolchain through `GOTOOLCHAIN=auto`.
- Add dependencies with `go get module-path@latest`; do not hand-select stale versions.
- Keep `internal/domain` free of HTTP and database imports.
- Add detailed Chinese comments for architectural decisions, concurrency cancellation, SQL safety, connection pooling, and lifecycle; do not narrate obvious assignments.
- Never expose arbitrary SQL, credentials, DSNs, or raw driver errors through HTTP.
- Use exact decimal arithmetic for money and encode money as a JSON string.
- Treat API date ranges as inclusive natural dates in `Asia/Shanghai`.
- Commit and push each completed task before proceeding to the next task.

---

## File Map

```text
go.mod / go.sum                         Go module and locked dependencies
cmd/server/main.go                      Process entrypoint and signal lifecycle
internal/domain/model.go                Date ranges, source enum, metrics, segments
internal/domain/errors.go               Stable error categories
internal/app/query_service.go           Routing, concurrent execution, merge
internal/app/observer.go                Bounded query-observation interface
internal/adapter/memory/repository.go    Deterministic local demo repository
internal/adapter/sqlrepo/repository.go   Shared SQL execution and row mapping
internal/adapter/sqlrepo/statements.go   Bound and Hive-safe SQL builders
internal/database/open.go                Hive, Trino, StarRocks connection factories
internal/config/config.go                Strong environment configuration
internal/httpapi/handler.go              Routes, DTOs, health and metrics
internal/httpapi/middleware.go           Request ID, recovery, auth, timeout, logging
internal/httpapi/errors.go               Stable HTTP error envelopes
internal/observability/metrics.go        Prometheus HTTP and query metrics
internal/bootstrap/app.go                Dependency assembly and resource ownership
api/openapi.yaml                         OpenAPI 3.1 contract
deploy/hive/schema.sql                   Hive aggregate tables and demo rows
deploy/starrocks/schema.sql              StarRocks aggregate tables and demo rows
.env.example                             Safe configuration template
Dockerfile                               Multi-stage non-root image
Makefile                                 Developer workflow
README.md                                End-to-end Chinese learning guide
```

Tests live next to their packages as `*_test.go`; `tests/smoke/real_sources_test.go` contains opt-in real-cluster tests.

---

### Task 1: Module and Domain Contract

**Files:**
- Create: `go.mod`
- Create: `internal/domain/model.go`
- Create: `internal/domain/model_test.go`
- Create: `internal/domain/errors.go`

**Interfaces:**
- Produces: `domain.Source`, `domain.DateRange`, `domain.Query`, `domain.RetailSummary`, `domain.RenovationFunnel`, `domain.Segment`, `domain.Repository`.
- Produces: sentinel errors `ErrInvalidArgument`, `ErrDependencyUnavailable`, `ErrOverflow`.

- [ ] **Step 1: Initialize the module and add exact-decimal support**

Run:

```bash
go mod init github.com/woo721/cursor_test
go mod edit -go=1.26.0 -toolchain=go1.26.5
go get github.com/shopspring/decimal@latest
```

Expected: `go.mod` declares the repository module, Go 1.26, toolchain 1.26.5, and the latest decimal module.

- [ ] **Step 2: Write failing domain tests**

Create `internal/domain/model_test.go` with table-driven tests that assert:

```go
func TestParseSource(t *testing.T) {
    tests := []struct {
        input string
        want  domain.Source
        ok    bool
    }{
        {"auto", domain.SourceAuto, true},
        {"hive", domain.SourceHive, true},
        {"trino", domain.SourceTrino, true},
        {"starrocks", domain.SourceStarRocks, true},
        {"memory", "", false},
        {"", "", false},
    }
    for _, tt := range tests {
        got, err := domain.ParseSource(tt.input)
        if tt.ok && (err != nil || got != tt.want) {
            t.Fatalf("ParseSource(%q) = %q, %v; want %q", tt.input, got, err, tt.want)
        }
        if !tt.ok && !errors.Is(err, domain.ErrInvalidArgument) {
            t.Fatalf("ParseSource(%q) error = %v; want ErrInvalidArgument", tt.input, err)
        }
    }
}

func TestNewDateRangeUsesInclusiveDatesAndLimitsSpan(t *testing.T) {
    loc, _ := time.LoadLocation("Asia/Shanghai")
    got, err := domain.NewDateRange("2026-08-01", "2026-08-03", 366, loc)
    if err != nil || got.Days() != 3 {
        t.Fatalf("range = %#v, %v; want three inclusive days", got, err)
    }
    _, err = domain.NewDateRange("2026-08-03", "2026-08-01", 366, loc)
    if !errors.Is(err, domain.ErrInvalidArgument) {
        t.Fatalf("reverse range error = %v; want ErrInvalidArgument", err)
    }
}

func TestValidateOrgID(t *testing.T) {
    for _, valid := range []string{"ORG-001", "store_2", "A"} {
        if err := domain.ValidateOrgID(valid); err != nil {
            t.Fatalf("ValidateOrgID(%q) = %v", valid, err)
        }
    }
    for _, invalid := range []string{"", "组织1", "x' OR 1=1 --", strings.Repeat("a", 65)} {
        if !errors.Is(domain.ValidateOrgID(invalid), domain.ErrInvalidArgument) {
            t.Fatalf("ValidateOrgID(%q) should fail", invalid)
        }
    }
}
```

- [ ] **Step 3: Run the tests and observe the expected compile failure**

Run:

```bash
go test ./internal/domain
```

Expected: FAIL because `internal/domain` and its exported types do not exist.

- [ ] **Step 4: Implement the domain contract**

Create `internal/domain/errors.go`:

```go
package domain

import "errors"

var (
    ErrInvalidArgument      = errors.New("invalid argument")
    ErrDependencyUnavailable = errors.New("dependency unavailable")
    ErrOverflow             = errors.New("numeric overflow")
)
```

Create `internal/domain/model.go` with:

```go
type Source string

const (
    SourceAuto      Source = "auto"
    SourceHive      Source = "hive"
    SourceTrino     Source = "trino"
    SourceStarRocks Source = "starrocks"
)

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
    LeadCount, InvitedCount, MeasuredCount int64
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
```

Implement:

- `ParseSource` as an explicit switch over the four public values.
- `NewDateRange` using `time.ParseInLocation("2006-01-02", ...)`, inclusive day count, reverse-range rejection, and `maxDays`.
- `DateRange.Days()`, `DateRange.StartString()`, and `DateRange.EndString()`.
- `ValidateOrgID` with the precompiled regex `^[A-Za-z0-9_-]{1,64}$`.

Add Chinese comments explaining why dates are normalized to natural days and why `memory` is not a public source.

- [ ] **Step 5: Run domain tests and static formatting**

Run:

```bash
gofmt -w internal/domain
go test ./internal/domain
go vet ./internal/domain
```

Expected: all commands exit 0.

- [ ] **Step 6: Commit and push**

```bash
git add go.mod go.sum internal/domain
git commit -m "feat: define analytics query domain"
git push -u origin cursor/go-data-query-api-0d0d
```

---

### Task 2: Automatic Routing and Concurrent Merge

**Files:**
- Create: `internal/app/observer.go`
- Create: `internal/app/query_service.go`
- Create: `internal/app/query_service_test.go`

**Interfaces:**
- Consumes: `domain.Repository`, `domain.Query`, and domain result types.
- Produces: `app.QueryService`, `app.RetailResult`, `app.RenovationResult`, `app.Clock`, `app.QueryObserver`.

- [ ] **Step 1: Add current concurrency and test-comparison dependencies**

Run:

```bash
go get golang.org/x/sync@latest
go get github.com/google/go-cmp@latest
```

Expected: `go.mod` records versions compatible with the declared Go toolchain.

- [ ] **Step 2: Write routing and merge tests with a recording repository**

The test helper records source calls and can block until its context is cancelled:

```go
type fakeClock struct{ now time.Time }
func (c fakeClock) Now() time.Time { return c.now }

type fakeRepository struct {
    retail domain.RetailSummary
    funnel domain.RenovationFunnel
    err error
    called chan domain.Query
}
```

Add focused tests:

1. `2026-07-01..2026-08-03` routes only to configured Hive history.
2. `2026-08-04..2026-08-06` routes only to StarRocks.
3. `2026-07-01..2026-08-06` creates non-overlapping Hive and StarRocks segments and merges:

```go
if got.Data.OrderCount != 15 ||
   !got.Data.SalesAmount.Equal(decimal.RequireFromString("35.50")) {
    t.Fatalf("merged retail = %#v", got.Data)
}
if diff := cmp.Diff(wantSegments, got.Segments); diff != "" {
    t.Fatalf("segments mismatch (-want +got):\n%s", diff)
}
```

4. Explicit `SourceTrino` uses one full-range segment.
5. When one repository fails, the blocking peer observes `ctx.Done()` and the result returns no partial metrics.
6. Adding two `math.MaxInt64` counts returns `domain.ErrOverflow`.

- [ ] **Step 3: Run service tests and observe failure**

Run:

```bash
go test ./internal/app
```

Expected: FAIL because `QueryService` is undefined.

- [ ] **Step 4: Add observer and service contracts**

In `observer.go`:

```go
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
```

In `query_service.go`, define:

```go
type Clock interface{ Now() time.Time }
type SystemClock struct{}
func (SystemClock) Now() time.Time { return time.Now() }

type QueryService struct {
    repositories map[domain.Source]domain.Repository
    historical   domain.Source
    realtimeDays int
    timezone     *time.Location
    clock        Clock
    observer     QueryObserver
}

type RetailResult struct {
    Data     domain.RetailSummary
    Segments []domain.Segment
}
type RenovationResult struct {
    Data     domain.RenovationFunnel
    Segments []domain.Segment
}
```

The constructor must reject:

- historical source other than Hive or Trino;
- realtime window less than 1;
- missing required repositories;
- nil timezone, clock, or observer.

- [ ] **Step 5: Implement planning, cancellation, and overflow-safe merge**

Implement `Plan(requested Source, range DateRange) ([]Segment, error)`:

```go
cutoff := beginningOfDay(s.clock.Now().In(s.timezone)).
    AddDate(0, 0, -(s.realtimeDays - 1))
```

For cross-boundary queries, return history ending at `cutoff.AddDate(0, 0, -1)` and StarRocks starting at `cutoff`.

Implement `RetailSummary` and `RenovationFunnel` with `errgroup.WithContext`. Each goroutine:

1. starts a timer;
2. queries its segment repository;
3. observes bounded source/metric labels;
4. returns the wrapped error.

Only merge after `group.Wait()` succeeds. Implement `safeAddInt64` using explicit bounds checks, and decimal addition using `decimal.Add`.

Add Chinese comments explaining why partial data is rejected and how `errgroup` propagates cancellation.

- [ ] **Step 6: Verify service behavior and race safety**

Run:

```bash
gofmt -w internal/app
go test -race ./internal/app
go vet ./internal/app
```

Expected: all commands exit 0; cancellation test completes without sleeps or race warnings.

- [ ] **Step 7: Commit and push**

```bash
git add internal/app go.mod go.sum
git commit -m "feat: route and merge analytics queries"
git push -u origin cursor/go-data-query-api-0d0d
```

---

### Task 3: Deterministic Memory Adapter

**Files:**
- Create: `internal/adapter/memory/repository.go`
- Create: `internal/adapter/memory/repository_test.go`

**Interfaces:**
- Consumes: `domain.Repository`.
- Produces: `memory.New() *Repository`, a deterministic implementation for local use.

- [ ] **Step 1: Write failing adapter tests**

Assert that:

- the same query always returns the same values;
- extending the range by one day equals the sum of the shorter range and that day;
- two organizations produce distinct deterministic values;
- retail amount has exactly two decimal places;
- `Ping` succeeds unless the context is cancelled.

Use the expected daily formulas in tests:

```go
seed := stableOrgSeed("ORG-001")
orders := int64(60 + (dayNumber+seed)%41)
priceCents := int64(129900 + (seed % 70000))
salesCents := orders * priceCents
```

For renovation, derive descending but non-negative daily stages from leads:

```text
leads = 35 + offset
invited = leads * 72 / 100
measured = invited * 64 / 100
signed = measured * 46 / 100
started = signed * 78 / 100
completed = started * 74 / 100
```

- [ ] **Step 2: Run the tests and observe failure**

Run:

```bash
go test ./internal/adapter/memory
```

Expected: FAIL because the memory repository does not exist.

- [ ] **Step 3: Implement deterministic daily aggregation**

Implement `Repository` without mutable shared state. Hash `org_id` with FNV-1a, derive each day number from UTC midnight, iterate over inclusive dates, and accumulate:

```go
amount := decimal.NewFromInt(salesCents).Shift(-2)
total.SalesAmount = total.SalesAmount.Add(amount)
```

Check the context once per day and return `ctx.Err()` immediately on cancellation. Use the domain’s overflow-safe behavior by keeping generated values bounded well below `int64` limits.

- [ ] **Step 4: Verify**

Run:

```bash
gofmt -w internal/adapter/memory
go test -race ./internal/adapter/memory
go test ./internal/app ./internal/domain
```

Expected: all tests pass.

- [ ] **Step 5: Commit and push**

```bash
git add internal/adapter/memory
git commit -m "feat: add deterministic memory data source"
git push -u origin cursor/go-data-query-api-0d0d
```

---

### Task 4: Shared SQL Repository and Dialect-Safe Statements

**Files:**
- Create: `internal/adapter/sqlrepo/statements.go`
- Create: `internal/adapter/sqlrepo/statements_test.go`
- Create: `internal/adapter/sqlrepo/repository.go`
- Create: `internal/adapter/sqlrepo/repository_test.go`

**Interfaces:**
- Consumes: `domain.Repository`, `database/sql`-compatible query execution.
- Produces: `sqlrepo.New(source, db, builder)`, `sqlrepo.NewBoundBuilder(...)`, `sqlrepo.NewHiveBuilder(...)`.

- [ ] **Step 1: Add latest SQL testing dependency and write failing statement tests**

Run:

```bash
go get github.com/DATA-DOG/go-sqlmock@latest
```

Define the desired interface in tests:

```go
type Statement struct {
    SQL  string
    Args []any
}
type Builder interface {
    Retail(domain.Query) Statement
    Renovation(domain.Query) Statement
}
```

Tests must assert:

- bound builders produce `?` arguments in `org_id`, start, end order;
- Hive builders produce no args and only fixed-format quoted literals;
- constructors reject unsafe catalog, schema, database, or table identifiers;
- retail SQL uses `COALESCE(SUM(order_count), 0)` and decimal-preserving sales aggregation;
- renovation SQL sums all six stage columns.

- [ ] **Step 2: Observe statement test failure**

Run:

```bash
go test ./internal/adapter/sqlrepo -run 'Test.*Builder'
```

Expected: FAIL because builders are undefined.

- [ ] **Step 3: Implement SQL builders**

Use:

```go
var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
```

`NewBoundBuilder(prefix, retailTable, renovationTable)` validates every identifier and renders:

```sql
SELECT
  COALESCE(SUM(order_count), 0),
  CAST(COALESCE(SUM(sales_amount), 0) AS DECIMAL(38, 2))
FROM analytics.dws_retail_order_daily
WHERE org_id = ? AND stat_date >= ? AND stat_date <= ?
```

`NewHiveBuilder` renders the same fixed query but substitutes only values already validated by domain types. Add an unexported `quoteHiveLiteral` that rejects any byte outside the already accepted org/date character sets before surrounding the value with single quotes. Never accept arbitrary conditions or SQL fragments.

- [ ] **Step 4: Write failing repository row-mapping tests**

With `sqlmock`, test:

- retail row `(12, "1234.50")` becomes exact decimal;
- renovation six integers map in order;
- empty aggregate row produces zeros;
- malformed decimal produces a wrapped `ErrDependencyUnavailable`;
- query error and scan error become `ErrDependencyUnavailable`;
- cancelled context remains `context.Canceled`, not 503-class dependency failure;
- `Ping` delegates to `PingContext`.

- [ ] **Step 5: Implement repository execution**

Define the minimal DB port:

```go
type DB interface {
    QueryRowContext(context.Context, string, ...any) *sql.Row
    PingContext(context.Context) error
}
```

`Repository` stores a bounded `domain.Source`, `DB`, and `Builder`. It must:

- call `QueryRowContext` with the builder’s SQL and args;
- scan retail decimal into a string, then call `decimal.NewFromString`;
- preserve `context.Canceled` and `context.DeadlineExceeded`;
- wrap all other query/scan/parse errors with `domain.ErrDependencyUnavailable` and source context;
- never include SQL text or args in the error.

- [ ] **Step 6: Verify SQL adapters**

Run:

```bash
gofmt -w internal/adapter/sqlrepo
go test -race ./internal/adapter/sqlrepo
go vet ./internal/adapter/sqlrepo
```

Expected: all tests pass.

- [ ] **Step 7: Commit and push**

```bash
git add internal/adapter/sqlrepo go.mod go.sum
git commit -m "feat: add safe SQL query adapters"
git push -u origin cursor/go-data-query-api-0d0d
```

---

### Task 5: Configuration and Real Database Connections

**Files:**
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`
- Create: `internal/database/open.go`
- Create: `internal/database/open_test.go`

**Interfaces:**
- Produces: `config.Load(getenv func(string) string) (Config, error)`.
- Produces: `database.OpenHive`, `database.OpenTrino`, `database.OpenStarRocks`, each returning `*sql.DB`.

- [ ] **Step 1: Write failing strong-config tests**

Test:

- documented defaults for address, timezone, timeouts, maximum days, real-time window, and pool settings;
- missing `API_KEY` always fails;
- `DATA_MODE=real` requires all three source configurations so explicit source queries remain available;
- invalid durations, booleans, ports, historical source, TLS files, and table identifiers fail at startup;
- secrets are redacted by `Config.LogValue()`.

The config shape is:

```go
type Config struct {
    Environment string
    Server      Server
    Query       Query
    DataMode    string
    APIKey      string
    Tables      Tables
    Hive        Hive
    Trino       Trino
    StarRocks   StarRocks
}
```

- [ ] **Step 2: Run config tests and observe failure**

Run:

```bash
go test ./internal/config
```

Expected: FAIL because `config.Load` is undefined.

- [ ] **Step 3: Implement environment parsing and validation**

Implement small helpers `required`, `stringDefault`, `intDefault`, `durationDefault`, and `boolDefault`. Parse `APP_TIMEZONE` with `time.LoadLocation`. Keep secrets as plain strings only inside the process; `LogValue` returns a map containing `"[REDACTED]"` for every password and key.

Use exact defaults from the spec:

```text
APP_ADDR=:8080
APP_TIMEZONE=Asia/Shanghai
QUERY_TIMEOUT=10s
SHUTDOWN_TIMEOUT=15s
MAX_QUERY_DAYS=366
REALTIME_WINDOW_DAYS=3
HISTORICAL_SOURCE=hive
```

- [ ] **Step 4: Add latest production drivers**

Run:

```bash
go get github.com/beltran/gohive/v2@latest
go get github.com/trinodb/trino-go-client/trino@latest
go get github.com/go-sql-driver/mysql@latest
```

Expected: dependency resolution succeeds under the declared Go toolchain.

- [ ] **Step 5: Write connection-factory tests before implementation**

Tests should avoid a real network and verify constructor inputs through injectable connector functions:

- Hive uses `gohive.Config`, username/password, database, auth mode, transport mode, and TLS configuration.
- Trino builds a `url.URL` with escaped user info, catalog, schema, source name, TLS, and request timeout.
- StarRocks uses `mysql.Config` rather than string concatenation, with parse time, dial/read/write timeouts, and TLS registration.
- `configurePool` applies max-open, max-idle, max-lifetime, and max-idle-time.

- [ ] **Step 6: Implement connection factories**

Implement TLS loading in a shared helper:

```go
func loadTLS(caFile, certFile, keyFile, serverName string) (*tls.Config, error)
```

Require TLS 1.2 minimum, never set `InsecureSkipVerify`, and support optional mTLS certificate/key pairs.

Use:

- `gohive.OpenDB(gohive.Config{... TLSConfig: tlsConfig})`;
- `sql.Open("trino", trinoURL.String())`;
- `mysql.NewConnector(mysql.Config{...})` followed by `sql.OpenDB`.

Perform no Ping inside these factories; bootstrap owns startup Ping and timeout policy.

- [ ] **Step 7: Verify**

Run:

```bash
gofmt -w internal/config internal/database
go test -race ./internal/config ./internal/database
go vet ./internal/config ./internal/database
```

Expected: all tests pass and no test opens a network socket.

- [ ] **Step 8: Commit and push**

```bash
git add internal/config internal/database go.mod go.sum
git commit -m "feat: configure analytics data sources"
git push -u origin cursor/go-data-query-api-0d0d
```

---

### Task 6: HTTP API, Authentication, and Error Semantics

**Files:**
- Create: `internal/httpapi/errors.go`
- Create: `internal/httpapi/middleware.go`
- Create: `internal/httpapi/handler.go`
- Create: `internal/httpapi/handler_test.go`
- Create: `internal/httpapi/middleware_test.go`

**Interfaces:**
- Consumes: query-service interface, readiness checker, `http.Handler`.
- Produces: `httpapi.New(Dependencies) http.Handler`.

- [ ] **Step 1: Write failing endpoint contract tests**

Use `httptest.NewServer` and a fake query service. Cover:

- missing or wrong API key returns 401 with no `WWW-Authenticate` detail;
- valid retail and renovation requests map domain data and segment metadata exactly;
- money serializes as `"3568200.50"`;
- invalid dates, source, org ID, and query span return 400;
- dependency errors return 503, deadline errors return 504, unexpected errors return 500;
- every response includes `X-Request-ID`, and error body request ID matches the header;
- panic recovery returns 500 without panic text;
- `/health/live`, `/health/ready`, and `/metrics` do not require API key.

- [ ] **Step 2: Run tests and observe failure**

Run:

```bash
go test ./internal/httpapi
```

Expected: FAIL because the HTTP package is undefined.

- [ ] **Step 3: Implement stable JSON errors and DTOs**

Use one response writer:

```go
type errorEnvelope struct {
    Error struct {
        Code      string `json:"code"`
        Message   string `json:"message"`
        RequestID string `json:"request_id"`
    } `json:"error"`
}
```

Set `Content-Type: application/json; charset=utf-8`, call `WriteHeader` once, and use user-safe fixed messages. Log encoding failures but never attempt a second response.

- [ ] **Step 4: Implement middleware in explicit order**

Compose:

```text
recovery(requestID(accessLog(timeout(apiKey(routes)))))
```

Implementation requirements:

- request IDs are 16 cryptographically random bytes encoded as lowercase hex;
- hash configured and presented API keys with SHA-256, then use `subtle.ConstantTimeCompare`;
- timeout derives a child context for business endpoints only;
- access logs use route templates, not raw URLs, and exclude all headers;
- recovery logs panic plus stack server-side but returns only `INTERNAL_ERROR`.

- [ ] **Step 5: Implement handlers**

Parse query parameters once into:

```go
type parsedQuery struct {
    Source domain.Source
    Query  domain.Query
}
```

The handler owns date/org/source validation and delegates no HTTP types to the app service. Response metadata uses `requested_source` and exact segment dates. Money uses `StringFixed(2)`.

Readiness accepts:

```go
type Readiness interface{ Ping(context.Context) error }
```

and uses a short independent timeout.

- [ ] **Step 6: Verify API behavior**

Run:

```bash
gofmt -w internal/httpapi
go test -race ./internal/httpapi
go vet ./internal/httpapi
```

Expected: all tests pass.

- [ ] **Step 7: Commit and push**

```bash
git add internal/httpapi
git commit -m "feat: expose secured analytics HTTP API"
git push -u origin cursor/go-data-query-api-0d0d
```

---

### Task 7: Metrics, Bootstrap, and Graceful Lifecycle

**Files:**
- Create: `internal/observability/metrics.go`
- Create: `internal/observability/metrics_test.go`
- Create: `internal/bootstrap/app.go`
- Create: `internal/bootstrap/app_test.go`
- Create: `cmd/server/main.go`
- Create: `cmd/server/main_test.go`

**Interfaces:**
- Produces: Prometheus-backed `app.QueryObserver`.
- Produces: `bootstrap.Build(config.Config) (*Application, error)` and `Application.Close() error`.
- Produces: a thin executable with injectable `run(ctx, getenv, stdout)`.

- [ ] **Step 1: Add the current Prometheus dependency**

Run:

```bash
go get github.com/prometheus/client_golang@latest
```

- [ ] **Step 2: Write failing metrics tests**

Create a private Prometheus registry, observe successful and failed Hive queries, serve it through `promhttp.HandlerFor`, and assert:

```text
data_query_total{metric="retail",source="hive",status="success"} 1
data_query_total{metric="retail",source="hive",status="error"} 1
```

Also verify HTTP metrics use only method, route, and status labels; they must not contain org IDs or request IDs.

- [ ] **Step 3: Implement bounded metrics**

`Metrics` implements `app.QueryObserver`, owns counters and histograms, and exposes:

```go
func NewMetrics(reg prometheus.Registerer) *Metrics
func (m *Metrics) Handler(g prometheus.Gatherer) http.Handler
func (m *Metrics) HTTPMiddleware(next http.Handler) http.Handler
```

Map errors only to `success`, `timeout`, `cancelled`, or `error`.

- [ ] **Step 4: Write failing bootstrap tests**

Tests assert:

- memory mode maps Hive, Trino, and StarRocks sources to deterministic memory repositories and performs no network access;
- real mode opens all three configured databases, pings them concurrently with a startup timeout, and closes every opened DB on partial startup failure;
- readiness pings each unique active repository;
- `Application.Close` is idempotent.

Inject opener functions in a private `builders` struct so tests use fakes rather than package globals.

- [ ] **Step 5: Implement bootstrap ownership**

`Application` contains:

```go
type Application struct {
    Handler http.Handler
    closers []io.Closer
    closeOnce sync.Once
    closeErr error
}
```

Assembly order:

1. create a private Prometheus registry;
2. create repositories according to mode;
3. create query service with configured timezone and real-time window;
4. create readiness composite;
5. create HTTP handler and attach metrics middleware;
6. retain only resources the application owns.

On any build failure, close all already-opened resources before returning.

- [ ] **Step 6: Write failing lifecycle tests and implement main**

Test `run` with a cancelled context and an ephemeral listener factory. Assert:

- server starts, responds, and exits without `log.Fatal`;
- context cancellation calls `http.Server.Shutdown`;
- shutdown deadline uses configured timeout;
- application resources close after server shutdown;
- startup errors return to `main` as errors.

`main` creates `signal.NotifyContext` for `SIGINT`/`SIGTERM`, loads config, and calls `run`. Only `main` may call `os.Exit`.

- [ ] **Step 7: Verify whole Go tree**

Run:

```bash
gofmt -w cmd internal
go test -race ./...
go vet ./...
```

Expected: all commands exit 0.

- [ ] **Step 8: Commit and push**

```bash
git add cmd internal go.mod go.sum
git commit -m "feat: assemble observable query service"
git push -u origin cursor/go-data-query-api-0d0d
```

---

### Task 8: API Contract, Schemas, Packaging, and Learning Guide

**Files:**
- Create: `api/openapi.yaml`
- Create: `deploy/hive/schema.sql`
- Create: `deploy/starrocks/schema.sql`
- Create: `.env.example`
- Create: `Dockerfile`
- Create: `Makefile`
- Replace: `README.md`
- Create: `tests/smoke/real_sources_test.go`

**Interfaces:**
- Documents the two business endpoints and three operational endpoints.
- Provides opt-in real-source verification under build tag `integration`.

- [ ] **Step 1: Write the OpenAPI contract and validate it**

Describe:

- API key security scheme in `X-API-Key`;
- required date/org parameters and source enum;
- fixed two-decimal money string pattern;
- retail, renovation, segment, metadata, and error schemas;
- 200/400/401/503/504 responses;
- unauthenticated health and metrics paths.

Run:

```bash
npx --yes @redocly/cli@latest lint api/openapi.yaml
```

Expected: no errors.

- [ ] **Step 2: Add engine-specific DDL and executable examples**

Hive DDL must partition by `stat_date`; StarRocks DDL must partition by date and use a key/model suitable for organization/date aggregation. Include idempotent inserts for `ORG-001` across historical and recent example dates.

Every metric column is non-null, count columns are BIGINT, and sales is DECIMAL(18,2). Add comments explaining the non-overlap contract between historical and real-time storage.

- [ ] **Step 3: Add opt-in smoke tests**

At the top:

```go
//go:build integration
```

The test loads real configuration, opens each source, runs one-day retail and renovation queries for `SMOKE_ORG_ID`, and asserts non-negative counts. If required environment variables are absent, call `t.Skip` with the exact missing variable names.

Run without integration tag:

```bash
go test ./...
```

Expected: regular tests pass without external services.

- [ ] **Step 4: Add safe local configuration and developer commands**

`.env.example` contains fake values only and documents every environment variable.

`Makefile` targets:

```make
.PHONY: fmt vet test test-race run build
fmt:
	gofmt -w cmd internal tests
vet:
	go vet ./...
test:
	go test ./...
test-race:
	go test -race ./...
run:
	API_KEY=$${API_KEY:-local-development-key} DATA_MODE=$${DATA_MODE:-memory} go run ./cmd/server
build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/query-api ./cmd/server
```

- [ ] **Step 5: Add the non-root Docker image**

Use `golang:1.26.5` for build and `gcr.io/distroless/static-debian12:nonroot` for runtime. Build with `CGO_ENABLED=0`, copy timezone data and CA certificates, expose 8080, run as non-root, and set the binary as the entrypoint.

Build:

```bash
docker build -t home-improvement-query-api:test .
```

Expected: image builds successfully and its configured user is non-root.

- [ ] **Step 6: Write the Chinese end-to-end README**

Explain in this order:

1. business story and why Hive/StarRocks are split;
2. Go package and request-flow map;
3. prerequisites and toolchain auto-download;
4. `make run` memory demo;
5. curl examples for both APIs and each source;
6. reading the router, interface, adapter, middleware, and bootstrap code;
7. testing commands and how TDD shaped the interfaces;
8. HiveServer2 username/password/TLS setup;
9. Trino-to-Hive setup;
10. StarRocks setup;
11. DDL loading and cutoff/non-overlap contract;
12. production deployment checklist;
13. Kerberos and Cohort conversion-rate extension points.

Include no real credentials and do not claim Docker Compose starts the external clusters.

- [ ] **Step 7: Verify docs and packaging**

Run:

```bash
git diff --check
make fmt
make vet
make test-race
docker build -t home-improvement-query-api:test .
```

Expected: all commands exit 0.

- [ ] **Step 8: Commit and push**

```bash
git add api deploy tests .env.example Dockerfile Makefile README.md
git commit -m "docs: add deployment and learning workflow"
git push -u origin cursor/go-data-query-api-0d0d
```

---

### Task 9: End-to-End Demo and Final Verification

**Files:**
- Modify only files required by a failing verification; every fix must start with a reproducing test.

**Interfaces:**
- Verifies the complete memory-mode application from process startup through HTTP response.

- [ ] **Step 1: Start the service in a persistent session**

Run in tmux:

```bash
API_KEY=demo-key DATA_MODE=memory APP_ADDR=:8080 go run ./cmd/server
```

Wait until `/health/ready` returns 200. Do not kill the service after verification.

- [ ] **Step 2: Exercise both business APIs**

Run:

```bash
curl -fsS -H 'X-API-Key: demo-key' \
  'http://127.0.0.1:8080/api/v1/retail/summary?start_date=2026-08-01&end_date=2026-08-06&source=auto&org_id=ORG-001'

curl -fsS -H 'X-API-Key: demo-key' \
  'http://127.0.0.1:8080/api/v1/renovation/funnel?start_date=2026-08-01&end_date=2026-08-06&source=auto&org_id=ORG-001'
```

Expected:

- both return 200 and valid JSON;
- retail money has two decimal places;
- renovation has all six stages;
- each response has an `X-Request-ID`;
- `meta.segments` matches the current configured three-day cutoff.

- [ ] **Step 3: Exercise security and failure contracts**

Verify:

- missing key returns 401;
- invalid org ID returns 400;
- a request longer than 366 days returns 400;
- `/health/live`, `/health/ready`, and `/metrics` return 200 without API key;
- metrics contain bounded HTTP and data-query series.

- [ ] **Step 4: Run final automated gates**

Run:

```bash
git diff --check
go test -count=1 -race ./...
go vet ./...
go build ./cmd/server
docker build -t home-improvement-query-api:test .
```

Expected: every command exits 0 with no test, race, vet, or build errors.

- [ ] **Step 5: Review the branch diff**

Run:

```bash
git status --short
git diff --stat main...HEAD
git log --oneline main..HEAD
```

Expected: no uncommitted files; commits correspond to domain, routing, memory, SQL, configuration, HTTP, bootstrap, and documentation.

- [ ] **Step 6: Commit any test-driven corrections, push, and update the pull request**

If verification required changes:

```bash
git add -A
git commit -m "fix: address end-to-end verification findings"
git push -u origin cursor/go-data-query-api-0d0d
```

Update the pull request description with final test evidence and walkthrough artifacts.
