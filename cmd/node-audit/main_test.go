package main

import (
	"testing"
	"time"

	"vps-node-auditor/internal/domain"
)

func TestParseLookback(t *testing.T) {
	t.Parallel()
	tests := map[string]time.Duration{
		"24h": 24 * time.Hour,
		"7d":  7 * 24 * time.Hour,
		"30m": 30 * time.Minute,
	}
	for input, expected := range tests {
		actual, err := parseLookback(input)
		if err != nil {
			t.Fatalf("parseLookback(%q): %v", input, err)
		}
		if actual != expected {
			t.Fatalf("parseLookback(%q) = %v", input, actual)
		}
	}
	if _, err := parseLookback("0d"); err == nil {
		t.Fatal("parseLookback accepted zero")
	}
}

func TestProviderBytes(t *testing.T) {
	t.Parallel()
	if got := providerBytes(10, 20, "both"); got != 30 {
		t.Fatalf("both = %d", got)
	}
	if got := providerBytes(10, 20, "max"); got != 20 {
		t.Fatalf("max = %d", got)
	}
}

func TestCycleStartUsesPreviousMonthBeforeBillingDay(t *testing.T) {
	t.Parallel()
	location := time.FixedZone("UTC+8", 8*60*60)
	now := time.Date(2026, 8, 3, 12, 0, 0, 0, location)
	got := cycleStart(now, location, 5)
	want := time.Date(2026, 7, 5, 0, 0, 0, 0, location).UTC()
	if !got.Equal(want) {
		t.Fatalf("cycleStart() = %v, want %v", got, want)
	}
}

func TestSelectBucketAutoAndExplicit(t *testing.T) {
	tests := []struct {
		value    string
		lookback time.Duration
		want     domain.TimelineBucket
	}{
		{"auto", 24 * time.Hour, domain.BucketMinute},
		{"auto", 7 * 24 * time.Hour, domain.BucketHour},
		{"auto", 30 * 24 * time.Hour, domain.BucketDay},
		{"auto", 365 * 24 * time.Hour, domain.BucketMonth},
		{"hour", time.Hour, domain.BucketHour},
	}
	for _, test := range tests {
		got, err := selectBucket(test.value, test.lookback)
		if err != nil || got != test.want {
			t.Fatalf("selectBucket(%q) = %q, %v; want %q", test.value, got, err, test.want)
		}
	}
}
