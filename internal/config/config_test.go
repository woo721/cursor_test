package config_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/woo721/cursor_test/internal/config"
)

func baseEnv(overrides map[string]string) func(string) string {
	m := map[string]string{
		"API_KEY": "test-api-key",
	}
	for k, v := range overrides {
		m[k] = v
	}
	return func(key string) string {
		if v, ok := m[key]; ok {
			return v
		}
		return ""
	}
}

func TestLoad_Defaults(t *testing.T) {
	cfg, err := config.Load(baseEnv(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.Environment != "development" {
		t.Fatalf("Environment=%q", cfg.Environment)
	}
	if cfg.Server.Addr != ":8080" {
		t.Fatalf("Addr=%q", cfg.Server.Addr)
	}
	if cfg.Server.Timezone.String() != "Asia/Shanghai" {
		t.Fatalf("Timezone=%q", cfg.Server.Timezone)
	}
	if cfg.Server.ShutdownTimeout != 15*time.Second {
		t.Fatalf("ShutdownTimeout=%v", cfg.Server.ShutdownTimeout)
	}
	if cfg.Query.Timeout != 10*time.Second {
		t.Fatalf("Query.Timeout=%v", cfg.Query.Timeout)
	}
	if cfg.Query.MaxQueryDays != 366 {
		t.Fatalf("MaxQueryDays=%d", cfg.Query.MaxQueryDays)
	}
	if cfg.Query.RealtimeWindowDays != 3 {
		t.Fatalf("RealtimeWindowDays=%d", cfg.Query.RealtimeWindowDays)
	}
	if cfg.Query.HistoricalSource != "hive" {
		t.Fatalf("HistoricalSource=%q", cfg.Query.HistoricalSource)
	}
	if cfg.DataMode != "memory" {
		t.Fatalf("DataMode=%q", cfg.DataMode)
	}
	if cfg.APIKey != "test-api-key" {
		t.Fatalf("APIKey=%q", cfg.APIKey)
	}
	if cfg.Tables.Prefix != "analytics" ||
		cfg.Tables.Retail != "dws_retail_order_daily" ||
		cfg.Tables.Renovation != "dws_renovation_stage_daily" {
		t.Fatalf("Tables=%+v", cfg.Tables)
	}

	for _, pool := range []config.Pool{cfg.Hive.Pool, cfg.Trino.Pool, cfg.StarRocks.Pool} {
		if pool.MaxOpen != 10 || pool.MaxIdle != 5 ||
			pool.MaxLifetime != 30*time.Minute || pool.MaxIdleTime != 5*time.Minute {
			t.Fatalf("unexpected pool defaults: %+v", pool)
		}
	}
	if cfg.Hive.Port != 10000 || cfg.Hive.Auth != "NONE" || cfg.Hive.TransportMode != "binary" {
		t.Fatalf("Hive defaults: %+v", cfg.Hive)
	}
	if cfg.Trino.Port != 8080 || cfg.Trino.Source != "home-improvement-query-api" {
		t.Fatalf("Trino defaults: %+v", cfg.Trino)
	}
	if cfg.StarRocks.Port != 9030 ||
		cfg.StarRocks.DialTimeout != 10*time.Second ||
		cfg.StarRocks.ReadTimeout != 10*time.Second ||
		cfg.StarRocks.WriteTimeout != 10*time.Second {
		t.Fatalf("StarRocks defaults: %+v", cfg.StarRocks)
	}
}

func TestLoad_MissingAPIKey(t *testing.T) {
	_, err := config.Load(func(string) string { return "" })
	if err == nil {
		t.Fatal("expected error for missing API_KEY")
	}
	if !strings.Contains(err.Error(), "API_KEY") {
		t.Fatalf("error should mention API_KEY: %v", err)
	}
}

func TestLoad_RealModeRequiresAllSources(t *testing.T) {
	_, err := config.Load(baseEnv(map[string]string{
		"DATA_MODE":  "real",
		"HIVE_HOST":  "hive.example",
		"TRINO_HOST": "trino.example",
		// missing StarRocks
	}))
	if err == nil {
		t.Fatal("expected error when StarRocks missing in real mode")
	}

	_, err = config.Load(baseEnv(map[string]string{
		"DATA_MODE":          "real",
		"HIVE_HOST":          "hive.example",
		"HIVE_DATABASE":      "analytics",
		"TRINO_HOST":         "trino.example",
		"TRINO_CATALOG":      "hive",
		"TRINO_SCHEMA":       "analytics",
		"TRINO_USER":         "trino",
		"STARROCKS_HOST":     "sr.example",
		"STARROCKS_DATABASE": "analytics",
		"STARROCKS_USERNAME": "root",
	}))
	if err != nil {
		t.Fatalf("Load real mode: %v", err)
	}
}

func TestLoad_InvalidValues(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"duration", map[string]string{"QUERY_TIMEOUT": "not-a-duration"}, "QUERY_TIMEOUT"},
		{"boolean", map[string]string{"HIVE_TLS_ENABLED": "maybe"}, "HIVE_TLS_ENABLED"},
		{"port", map[string]string{"HIVE_PORT": "70000"}, "HIVE_PORT"},
		{"port_zero", map[string]string{"TRINO_PORT": "0"}, "TRINO_PORT"},
		{"historical", map[string]string{"HISTORICAL_SOURCE": "starrocks"}, "HISTORICAL_SOURCE"},
		{"table", map[string]string{"TABLE_RETAIL": "bad-name!"}, "TABLE_RETAIL"},
		{"data_mode", map[string]string{"DATA_MODE": "demo"}, "DATA_MODE"},
		{"max_days", map[string]string{"MAX_QUERY_DAYS": "0"}, "MAX_QUERY_DAYS"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.Load(baseEnv(tc.env))
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q should contain %q", err, tc.want)
			}
		})
	}
}

func TestLoad_InvalidTLSFiles(t *testing.T) {
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(ca, []byte("ca"), 0o600); err != nil {
		t.Fatal(err)
	}
	cert := filepath.Join(dir, "cert.pem")
	if err := os.WriteFile(cert, []byte("cert"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := config.Load(baseEnv(map[string]string{
		"HIVE_TLS_ENABLED": "true",
		"HIVE_TLS_CA_FILE": filepath.Join(dir, "missing-ca.pem"),
	}))
	if err == nil || !strings.Contains(err.Error(), "HIVE_TLS") {
		t.Fatalf("expected missing CA error, got %v", err)
	}

	_, err = config.Load(baseEnv(map[string]string{
		"TRINO_TLS_ENABLED":   "true",
		"TRINO_TLS_CERT_FILE": cert,
		// key missing
	}))
	if err == nil || !strings.Contains(err.Error(), "TRINO_TLS") {
		t.Fatalf("expected cert/key pair error, got %v", err)
	}

	_, err = config.Load(baseEnv(map[string]string{
		"STARROCKS_TLS_ENABLED": "true",
		"STARROCKS_TLS_CA_FILE": ca,
	}))
	if err != nil {
		t.Fatalf("CA-only TLS should be valid: %v", err)
	}
}

func TestConfig_LogValue_RedactsSecrets(t *testing.T) {
	cfg, err := config.Load(baseEnv(map[string]string{
		"API_KEY":            "super-secret-api-key",
		"HIVE_PASSWORD":      "hive-pass",
		"TRINO_PASSWORD":     "trino-pass",
		"STARROCKS_PASSWORD": "sr-pass",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	rendered := fmt.Sprint(cfg.LogValue().Any())
	for _, secret := range []string{"super-secret-api-key", "hive-pass", "trino-pass", "sr-pass"} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("secret %q leaked in LogValue: %s", secret, rendered)
		}
	}
	if !strings.Contains(rendered, "[REDACTED]") {
		t.Fatalf("expected [REDACTED] in LogValue: %s", rendered)
	}
}
