package sqlrepo_test

import (
	"strings"
	"testing"
	"time"

	"github.com/woo721/cursor_test/internal/adapter/sqlrepo"
	"github.com/woo721/cursor_test/internal/domain"
)

func sampleQuery(t *testing.T) domain.Query {
	t.Helper()
	loc := time.UTC
	dr, err := domain.NewDateRange("2026-08-01", "2026-08-06", 366, loc)
	if err != nil {
		t.Fatalf("NewDateRange: %v", err)
	}
	return domain.Query{Range: dr, OrgID: "ORG_001"}
}

func TestBoundBuilder_RetailUsesPlaceholders(t *testing.T) {
	b, err := sqlrepo.NewBoundBuilder("analytics", "dws_retail_order_daily", "dws_renovation_stage_daily")
	if err != nil {
		t.Fatalf("NewBoundBuilder: %v", err)
	}
	q := sampleQuery(t)
	stmt := b.Retail(q)

	if !strings.Contains(stmt.SQL, "COALESCE(SUM(order_count), 0)") {
		t.Fatalf("retail SQL missing order_count coalesce: %s", stmt.SQL)
	}
	if !strings.Contains(stmt.SQL, "CAST(COALESCE(SUM(sales_amount), 0) AS DECIMAL(38, 2))") {
		t.Fatalf("retail SQL missing decimal-preserving sales aggregate: %s", stmt.SQL)
	}
	if !strings.Contains(stmt.SQL, "FROM analytics.dws_retail_order_daily") {
		t.Fatalf("retail SQL missing qualified table: %s", stmt.SQL)
	}
	if !strings.Contains(stmt.SQL, "WHERE org_id = ? AND stat_date >= ? AND stat_date <= ?") {
		t.Fatalf("retail SQL missing bound predicates: %s", stmt.SQL)
	}
	if len(stmt.Args) != 3 {
		t.Fatalf("want 3 args, got %#v", stmt.Args)
	}
	if stmt.Args[0] != q.OrgID {
		t.Fatalf("args[0] org_id: got %#v want %q", stmt.Args[0], q.OrgID)
	}
	if stmt.Args[1] != q.Range.StartString() {
		t.Fatalf("args[1] start: got %#v want %q", stmt.Args[1], q.Range.StartString())
	}
	if stmt.Args[2] != q.Range.EndString() {
		t.Fatalf("args[2] end: got %#v want %q", stmt.Args[2], q.Range.EndString())
	}
}

func TestBoundBuilder_RenovationSumsSixStages(t *testing.T) {
	b, err := sqlrepo.NewBoundBuilder("analytics", "dws_retail_order_daily", "dws_renovation_stage_daily")
	if err != nil {
		t.Fatalf("NewBoundBuilder: %v", err)
	}
	q := sampleQuery(t)
	stmt := b.Renovation(q)

	for _, col := range []string{
		"COALESCE(SUM(lead_count), 0)",
		"COALESCE(SUM(invited_count), 0)",
		"COALESCE(SUM(measured_count), 0)",
		"COALESCE(SUM(signed_count), 0)",
		"COALESCE(SUM(started_count), 0)",
		"COALESCE(SUM(completed_count), 0)",
	} {
		if !strings.Contains(stmt.SQL, col) {
			t.Fatalf("renovation SQL missing %s: %s", col, stmt.SQL)
		}
	}
	if !strings.Contains(stmt.SQL, "FROM analytics.dws_renovation_stage_daily") {
		t.Fatalf("renovation SQL missing qualified table: %s", stmt.SQL)
	}
	if !strings.Contains(stmt.SQL, "WHERE org_id = ? AND stat_date >= ? AND stat_date <= ?") {
		t.Fatalf("renovation SQL missing bound predicates: %s", stmt.SQL)
	}
	if len(stmt.Args) != 3 {
		t.Fatalf("want 3 args, got %#v", stmt.Args)
	}
	if stmt.Args[0] != q.OrgID || stmt.Args[1] != q.Range.StartString() || stmt.Args[2] != q.Range.EndString() {
		t.Fatalf("args order want org,start,end got %#v", stmt.Args)
	}
}

func TestHiveBuilder_RetailEmbedsQuotedLiterals(t *testing.T) {
	b, err := sqlrepo.NewHiveBuilder("analytics", "dws_retail_order_daily", "dws_renovation_stage_daily")
	if err != nil {
		t.Fatalf("NewHiveBuilder: %v", err)
	}
	q := sampleQuery(t)
	stmt := b.Retail(q)

	if len(stmt.Args) != 0 {
		t.Fatalf("Hive builder must not produce Args, got %#v", stmt.Args)
	}
	if strings.Contains(stmt.SQL, "?") {
		t.Fatalf("Hive SQL must not use placeholders: %s", stmt.SQL)
	}
	if !strings.Contains(stmt.SQL, "COALESCE(SUM(order_count), 0)") {
		t.Fatalf("Hive retail SQL missing order_count coalesce: %s", stmt.SQL)
	}
	if !strings.Contains(stmt.SQL, "CAST(COALESCE(SUM(sales_amount), 0) AS DECIMAL(38, 2))") {
		t.Fatalf("Hive retail SQL missing decimal sales: %s", stmt.SQL)
	}
	wantPred := "WHERE org_id = 'ORG_001' AND stat_date >= '2026-08-01' AND stat_date <= '2026-08-06'"
	if !strings.Contains(stmt.SQL, wantPred) {
		t.Fatalf("Hive retail predicates: got %s want substring %s", stmt.SQL, wantPred)
	}
}

func TestHiveBuilder_RenovationNoArgs(t *testing.T) {
	b, err := sqlrepo.NewHiveBuilder("analytics", "dws_retail_order_daily", "dws_renovation_stage_daily")
	if err != nil {
		t.Fatalf("NewHiveBuilder: %v", err)
	}
	q := sampleQuery(t)
	stmt := b.Renovation(q)

	if len(stmt.Args) != 0 {
		t.Fatalf("Hive builder must not produce Args, got %#v", stmt.Args)
	}
	for _, col := range []string{
		"COALESCE(SUM(lead_count), 0)",
		"COALESCE(SUM(invited_count), 0)",
		"COALESCE(SUM(measured_count), 0)",
		"COALESCE(SUM(signed_count), 0)",
		"COALESCE(SUM(started_count), 0)",
		"COALESCE(SUM(completed_count), 0)",
	} {
		if !strings.Contains(stmt.SQL, col) {
			t.Fatalf("Hive renovation SQL missing %s: %s", col, stmt.SQL)
		}
	}
	wantPred := "WHERE org_id = 'ORG_001' AND stat_date >= '2026-08-01' AND stat_date <= '2026-08-06'"
	if !strings.Contains(stmt.SQL, wantPred) {
		t.Fatalf("Hive renovation predicates: got %s", stmt.SQL)
	}
}

func TestBuilders_RejectUnsafeIdentifiers(t *testing.T) {
	unsafe := []struct {
		name                 string
		prefix, retail, reno string
	}{
		{"bad catalog/schema prefix", "analytics;drop", "dws_retail_order_daily", "dws_renovation_stage_daily"},
		{"bad retail table", "analytics", "dws_retail;drop", "dws_renovation_stage_daily"},
		{"bad renovation table", "analytics", "dws_retail_order_daily", "dws reno"},
		{"dotted prefix as one token", "hive.analytics", "dws_retail_order_daily", "dws_renovation_stage_daily"},
		{"empty prefix", "", "dws_retail_order_daily", "dws_renovation_stage_daily"},
		{"starts with digit", "1analytics", "dws_retail_order_daily", "dws_renovation_stage_daily"},
	}
	for _, tt := range unsafe {
		t.Run("bound/"+tt.name, func(t *testing.T) {
			if _, err := sqlrepo.NewBoundBuilder(tt.prefix, tt.retail, tt.reno); err == nil {
				t.Fatal("NewBoundBuilder: want error for unsafe identifier")
			}
		})
		t.Run("hive/"+tt.name, func(t *testing.T) {
			if _, err := sqlrepo.NewHiveBuilder(tt.prefix, tt.retail, tt.reno); err == nil {
				t.Fatal("NewHiveBuilder: want error for unsafe identifier")
			}
		})
	}
}
