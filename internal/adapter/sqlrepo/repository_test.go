package sqlrepo_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/shopspring/decimal"

	"github.com/woo721/cursor_test/internal/adapter/sqlrepo"
	"github.com/woo721/cursor_test/internal/domain"
)

func newTestRepo(t *testing.T, source domain.Source) (*sql.DB, sqlmock.Sqlmock, *sqlrepo.Repository) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	b, err := sqlrepo.NewBoundBuilder("analytics", "dws_retail_order_daily", "dws_renovation_stage_daily")
	if err != nil {
		t.Fatalf("NewBoundBuilder: %v", err)
	}
	repo := sqlrepo.New(source, db, b)
	return db, mock, repo
}

func TestRepository_RetailSummary_MapsDecimal(t *testing.T) {
	_, mock, repo := newTestRepo(t, domain.SourceStarRocks)
	q := sampleQuery(t)

	mock.ExpectQuery("SELECT").
		WithArgs(q.OrgID, q.Range.StartString(), q.Range.EndString()).
		WillReturnRows(sqlmock.NewRows([]string{"order_count", "sales_amount"}).AddRow(12, "1234.50"))

	got, err := repo.RetailSummary(context.Background(), q)
	if err != nil {
		t.Fatalf("RetailSummary: %v", err)
	}
	if got.OrderCount != 12 {
		t.Fatalf("OrderCount=%d", got.OrderCount)
	}
	want := decimal.RequireFromString("1234.50")
	if !got.SalesAmount.Equal(want) {
		t.Fatalf("SalesAmount=%s want %s", got.SalesAmount, want)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRepository_RenovationFunnel_MapsSixStages(t *testing.T) {
	_, mock, repo := newTestRepo(t, domain.SourceStarRocks)
	q := sampleQuery(t)

	mock.ExpectQuery("SELECT").
		WithArgs(q.OrgID, q.Range.StartString(), q.Range.EndString()).
		WillReturnRows(sqlmock.NewRows([]string{
			"lead_count", "invited_count", "measured_count",
			"signed_count", "started_count", "completed_count",
		}).AddRow(100, 80, 60, 40, 30, 20))

	got, err := repo.RenovationFunnel(context.Background(), q)
	if err != nil {
		t.Fatalf("RenovationFunnel: %v", err)
	}
	if got.LeadCount != 100 || got.InvitedCount != 80 || got.MeasuredCount != 60 ||
		got.SignedCount != 40 || got.StartedCount != 30 || got.CompletedCount != 20 {
		t.Fatalf("unexpected funnel: %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRepository_EmptyAggregateRow_Zeros(t *testing.T) {
	_, mock, repo := newTestRepo(t, domain.SourceTrino)
	q := sampleQuery(t)

	mock.ExpectQuery("SELECT").
		WithArgs(q.OrgID, q.Range.StartString(), q.Range.EndString()).
		WillReturnRows(sqlmock.NewRows([]string{"order_count", "sales_amount"}).AddRow(0, "0.00"))

	got, err := repo.RetailSummary(context.Background(), q)
	if err != nil {
		t.Fatalf("RetailSummary: %v", err)
	}
	if got.OrderCount != 0 || !got.SalesAmount.IsZero() {
		t.Fatalf("want zeros, got %+v", got)
	}

	mock.ExpectQuery("SELECT").
		WithArgs(q.OrgID, q.Range.StartString(), q.Range.EndString()).
		WillReturnRows(sqlmock.NewRows([]string{
			"lead_count", "invited_count", "measured_count",
			"signed_count", "started_count", "completed_count",
		}).AddRow(0, 0, 0, 0, 0, 0))

	funnel, err := repo.RenovationFunnel(context.Background(), q)
	if err != nil {
		t.Fatalf("RenovationFunnel: %v", err)
	}
	if funnel != (domain.RenovationFunnel{}) {
		t.Fatalf("want zero funnel, got %+v", funnel)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRepository_MalformedDecimal_DependencyUnavailable(t *testing.T) {
	_, mock, repo := newTestRepo(t, domain.SourceHive)
	q := sampleQuery(t)

	mock.ExpectQuery("SELECT").
		WithArgs(q.OrgID, q.Range.StartString(), q.Range.EndString()).
		WillReturnRows(sqlmock.NewRows([]string{"order_count", "sales_amount"}).AddRow(1, "not-a-decimal"))

	_, err := repo.RetailSummary(context.Background(), q)
	if !errors.Is(err, domain.ErrDependencyUnavailable) {
		t.Fatalf("want ErrDependencyUnavailable, got %v", err)
	}
	if !strings.Contains(err.Error(), string(domain.SourceHive)) {
		t.Fatalf("error should mention source: %v", err)
	}
	if strings.Contains(err.Error(), "SELECT") || strings.Contains(err.Error(), "ORG_001") {
		t.Fatalf("error must not leak SQL or args: %v", err)
	}
}

func TestRepository_QueryError_DependencyUnavailable(t *testing.T) {
	_, mock, repo := newTestRepo(t, domain.SourceStarRocks)
	q := sampleQuery(t)

	mock.ExpectQuery("SELECT").
		WithArgs(q.OrgID, q.Range.StartString(), q.Range.EndString()).
		WillReturnError(errors.New("connection refused"))

	_, err := repo.RetailSummary(context.Background(), q)
	if !errors.Is(err, domain.ErrDependencyUnavailable) {
		t.Fatalf("want ErrDependencyUnavailable, got %v", err)
	}
	if strings.Contains(err.Error(), "SELECT") || strings.Contains(err.Error(), "?") {
		t.Fatalf("error must not leak SQL: %v", err)
	}
}

func TestRepository_ScanError_DependencyUnavailable(t *testing.T) {
	_, mock, repo := newTestRepo(t, domain.SourceStarRocks)
	q := sampleQuery(t)

	// Wrong arity forces Scan failure.
	mock.ExpectQuery("SELECT").
		WithArgs(q.OrgID, q.Range.StartString(), q.Range.EndString()).
		WillReturnRows(sqlmock.NewRows([]string{"only_one"}).AddRow(1))

	_, err := repo.RetailSummary(context.Background(), q)
	if !errors.Is(err, domain.ErrDependencyUnavailable) {
		t.Fatalf("want ErrDependencyUnavailable, got %v", err)
	}
}

func TestRepository_CanceledContext_Preserved(t *testing.T) {
	_, mock, repo := newTestRepo(t, domain.SourceStarRocks)
	q := sampleQuery(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	mock.ExpectQuery("SELECT").
		WithArgs(q.OrgID, q.Range.StartString(), q.Range.EndString()).
		WillReturnError(context.Canceled)

	_, err := repo.RetailSummary(ctx, q)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if errors.Is(err, domain.ErrDependencyUnavailable) {
		t.Fatal("canceled must not become ErrDependencyUnavailable")
	}
}

func TestRepository_DeadlineExceeded_Preserved(t *testing.T) {
	_, mock, repo := newTestRepo(t, domain.SourceTrino)
	q := sampleQuery(t)

	mock.ExpectQuery("SELECT").
		WithArgs(q.OrgID, q.Range.StartString(), q.Range.EndString()).
		WillReturnError(context.DeadlineExceeded)

	_, err := repo.RenovationFunnel(context.Background(), q)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want context.DeadlineExceeded, got %v", err)
	}
	if errors.Is(err, domain.ErrDependencyUnavailable) {
		t.Fatal("deadline must not become ErrDependencyUnavailable")
	}
}

func TestRepository_Ping_DelegatesToPingContext(t *testing.T) {
	_, mock, repo := newTestRepo(t, domain.SourceStarRocks)
	mock.ExpectPing()
	if err := repo.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRepository_PingError_DependencyUnavailable(t *testing.T) {
	_, mock, repo := newTestRepo(t, domain.SourceHive)
	mock.ExpectPing().WillReturnError(errors.New("ping failed"))
	err := repo.Ping(context.Background())
	if !errors.Is(err, domain.ErrDependencyUnavailable) {
		t.Fatalf("want ErrDependencyUnavailable, got %v", err)
	}
	if !strings.Contains(err.Error(), string(domain.SourceHive)) {
		t.Fatalf("error should mention source: %v", err)
	}
}

func TestRepository_PingCanceled_Preserved(t *testing.T) {
	_, mock, repo := newTestRepo(t, domain.SourceStarRocks)
	mock.ExpectPing().WillReturnError(context.Canceled)
	err := repo.Ping(context.Background())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}
