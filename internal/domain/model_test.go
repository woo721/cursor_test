package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/woo721/cursor_test/internal/domain"
)

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
