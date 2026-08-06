package sqlrepo

import "testing"

func TestQuoteHiveLiteral_RejectsUnsafeBytes(t *testing.T) {
	if _, err := quoteHiveLiteral("ORG'001"); err == nil {
		t.Fatal("want reject single quote")
	}
	if _, err := quoteHiveLiteral("ORG;001"); err == nil {
		t.Fatal("want reject semicolon")
	}
	if _, err := quoteHiveLiteral("ORG 001"); err == nil {
		t.Fatal("want reject space")
	}
	got, err := quoteHiveLiteral("ORG_001")
	if err != nil {
		t.Fatalf("valid org: %v", err)
	}
	if got != "'ORG_001'" {
		t.Fatalf("got %q", got)
	}
	got, err = quoteHiveLiteral("2026-08-01")
	if err != nil {
		t.Fatalf("valid date: %v", err)
	}
	if got != "'2026-08-01'" {
		t.Fatalf("got %q", got)
	}
}
