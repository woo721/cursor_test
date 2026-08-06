//go:build integration

package smoke_test

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/woo721/cursor_test/internal/adapter/sqlrepo"
	"github.com/woo721/cursor_test/internal/config"
	"github.com/woo721/cursor_test/internal/database"
	"github.com/woo721/cursor_test/internal/domain"
)

var requiredRealEnv = []string{
	"API_KEY",
	"DATA_MODE",
	"HIVE_HOST",
	"HIVE_DATABASE",
	"TRINO_HOST",
	"TRINO_USER",
	"TRINO_CATALOG",
	"TRINO_SCHEMA",
	"STARROCKS_HOST",
	"STARROCKS_USERNAME",
	"STARROCKS_DATABASE",
	"SMOKE_ORG_ID",
}

func missingEnv(names []string) []string {
	var missing []string
	for _, name := range names {
		if strings.TrimSpace(os.Getenv(name)) == "" {
			missing = append(missing, name)
		}
	}
	return missing
}

func smokeDate(cfg config.Config) string {
	if v := strings.TrimSpace(os.Getenv("SMOKE_STAT_DATE")); v != "" {
		return v
	}
	return time.Now().In(cfg.Server.Timezone).Format("2006-01-02")
}

func TestRealSourcesRetailAndRenovation(t *testing.T) {
	if missing := missingEnv(requiredRealEnv); len(missing) > 0 {
		t.Skipf("missing required environment variables: %s", strings.Join(missing, ", "))
	}
	if os.Getenv("DATA_MODE") != "real" {
		t.Skipf("DATA_MODE must be real for integration smoke tests")
	}

	cfg, err := config.Load(os.Getenv)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	date := smokeDate(cfg)
	dr, err := domain.NewDateRange(date, date, cfg.Query.MaxQueryDays, cfg.Server.Timezone)
	if err != nil {
		t.Fatalf("SMOKE_STAT_DATE %q: %v", date, err)
	}
	q := domain.Query{Range: dr, OrgID: os.Getenv("SMOKE_ORG_ID")}

	hiveBuilder, err := sqlrepo.NewHiveBuilder(cfg.Tables.Prefix, cfg.Tables.Retail, cfg.Tables.Renovation)
	if err != nil {
		t.Fatalf("hive builder: %v", err)
	}
	boundBuilder, err := sqlrepo.NewBoundBuilder(cfg.Tables.Prefix, cfg.Tables.Retail, cfg.Tables.Renovation)
	if err != nil {
		t.Fatalf("bound builder: %v", err)
	}

	type sourceCase struct {
		source  domain.Source
		open    func() (*sql.DB, error)
		builder sqlrepo.Builder
	}
	cases := []sourceCase{
		{source: domain.SourceHive, open: func() (*sql.DB, error) { return database.OpenHive(cfg.Hive) }, builder: hiveBuilder},
		{source: domain.SourceTrino, open: func() (*sql.DB, error) { return database.OpenTrino(cfg.Trino) }, builder: boundBuilder},
		{source: domain.SourceStarRocks, open: func() (*sql.DB, error) { return database.OpenStarRocks(cfg.StarRocks) }, builder: boundBuilder},
	}

	for _, tc := range cases {
		t.Run(string(tc.source), func(t *testing.T) {
			db, err := tc.open()
			if err != nil {
				t.Fatalf("open %s: %v", tc.source, err)
			}
			defer db.Close()

			repo := sqlrepo.New(tc.source, db, tc.builder)
			ctx, cancel := context.WithTimeout(context.Background(), cfg.Query.Timeout)
			defer cancel()
			if err := repo.Ping(ctx); err != nil {
				t.Fatalf("ping %s: %v", tc.source, err)
			}

			retail, err := repo.RetailSummary(ctx, q)
			if err != nil {
				t.Fatalf("retail %s: %v", tc.source, err)
			}
			if retail.OrderCount < 0 {
				t.Fatalf("retail order_count=%d, want non-negative", retail.OrderCount)
			}

			renovation, err := repo.RenovationFunnel(ctx, q)
			if err != nil {
				t.Fatalf("renovation %s: %v", tc.source, err)
			}
			if renovation.LeadCount < 0 ||
				renovation.InvitedCount < 0 ||
				renovation.MeasuredCount < 0 ||
				renovation.SignedCount < 0 ||
				renovation.StartedCount < 0 ||
				renovation.CompletedCount < 0 {
				t.Fatalf("renovation counts=%+v, want non-negative", renovation)
			}
		})
	}
}
