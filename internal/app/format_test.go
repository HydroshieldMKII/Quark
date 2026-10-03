package app

import (
	"testing"
	"time"
)

func TestFormatTime(t *testing.T) {
	loc := time.FixedZone("EDT", -4*3600)
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, loc) // a Saturday
	tests := []struct {
		t    time.Time
		want string
	}{
		{time.Time{}, "—"},
		{time.Date(2026, 10, 3, 9, 5, 0, 0, loc), "Today 09:05"},
		{time.Date(2026, 10, 3, 3, 30, 0, 0, time.UTC), "Yesterday 23:30"}, // UTC morning is the previous evening here
		{time.Date(2026, 10, 2, 18, 0, 0, 0, loc), "Yesterday 18:00"},
		{time.Date(2026, 9, 28, 8, 0, 0, 0, loc), "Monday 08:00"},
		{time.Date(2026, 9, 26, 8, 0, 0, 0, loc), "26 Sep"},
		{time.Date(2025, 12, 31, 8, 0, 0, 0, loc), "31 Dec 2025"},
		{time.Date(2026, 10, 5, 8, 0, 0, 0, loc), "5 Oct"}, // future (clock skew)
	}
	for _, tt := range tests {
		if got := FormatTime(tt.t, now); got != tt.want {
			t.Errorf("FormatTime(%v) = %q, want %q", tt.t, got, tt.want)
		}
	}
}

func TestDescribeKind(t *testing.T) {
	if DescribeKind("", "x") != nil {
		t.Fatal("empty kind")
	}
	if u := DescribeKind("network", "Connection failed"); u.Action != ActionRetry || u.Technical != "Connection failed" {
		t.Fatalf("u = %+v", u)
	}
	if DescribeKind("cancelled", "") != nil {
		t.Fatal("cancelled")
	}
}
