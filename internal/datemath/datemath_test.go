package datemath

import (
	"testing"
	"time"

	"es-querycost/internal/query"
)

func TestParseDateMathNow(t *testing.T) {
	ref := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
	tm, ok := ParseDateMath("now", ref)
	if !ok || !tm.Equal(ref) {
		t.Errorf("expected now to equal reference time")
	}
}

func TestParseDateMathRelative(t *testing.T) {
	ref := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		input string
		want  time.Time
	}{
		{"now-14d", ref.Add(-14 * 24 * time.Hour)},
		{"now-1h", ref.Add(-time.Hour)},
		{"now+30m", ref.Add(30 * time.Minute)},
		{"now-1M", ref.Add(-30 * 24 * time.Hour)},
		{"now-1y", ref.Add(-365 * 24 * time.Hour)},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			tm, ok := ParseDateMath(tc.input, ref)
			if !ok {
				t.Fatalf("failed to parse %q", tc.input)
			}
			if !tm.Equal(tc.want) {
				t.Errorf("%q: got %v, want %v", tc.input, tm, tc.want)
			}
		})
	}
}

func TestParseDateMathAbsolute(t *testing.T) {
	ref := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
	tm, ok := ParseDateMath("2024-01-10", ref)
	if !ok {
		t.Fatalf("expected absolute date to parse")
	}
	want := time.Date(2024, 1, 10, 0, 0, 0, 0, time.UTC)
	if !tm.Equal(want) {
		t.Errorf("got %v, want %v", tm, want)
	}
}

func TestParseWindow(t *testing.T) {
	ref := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
	r := query.Range{Low: "now-14d", High: "now", InclusiveLow: true, InclusiveHigh: true}
	w, err := ParseWindow(r.Low, r.High, ref)
	if err != nil {
		t.Fatalf("parse window: %v", err)
	}
	d, ok := w.Duration()
	if !ok {
		t.Fatalf("expected bounded window")
	}
	if d != 14*24*time.Hour {
		t.Errorf("expected 14 days, got %v", d)
	}
}

func TestParseWindowInvalid(t *testing.T) {
	_, err := ParseWindow("invalid", "now", time.Now())
	if err == nil {
		t.Error("expected error for invalid low bound")
	}
}

func TestParseDurationHuman(t *testing.T) {
	tests := []struct {
		input string
		want  time.Duration
	}{
		{"14d", 14 * 24 * time.Hour},
		{"2w", 2 * 7 * 24 * time.Hour},
		{"1M", 30 * 24 * time.Hour},
		{"1y", 365 * 24 * time.Hour},
		{"30m", 30 * time.Minute},
		{"1h", time.Hour},
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			d, ok := ParseDurationHuman(tc.input)
			if !ok {
				t.Fatalf("failed to parse %q", tc.input)
			}
			if d != tc.want {
				t.Errorf("%q: got %v, want %v", tc.input, d, tc.want)
			}
		})
	}
}

func TestParseDurationHumanInvalid(t *testing.T) {
	_, ok := ParseDurationHuman("nope")
	if ok {
		t.Error("expected invalid duration to fail")
	}
}
