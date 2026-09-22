package datemath

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ParseDateMath parses a small subset of Elasticsearch date-math expressions and
// common absolute date formats. It returns the absolute time and a bool indicating
// whether parsing succeeded.
func ParseDateMath(input string, ref time.Time) (time.Time, bool) {
	input = strings.TrimSpace(input)
	if input == "*" || input == "" {
		return time.Time{}, false
	}

	// Normalise Elasticsearch rounding syntax: now-14d/d -> now-14d
	input = strings.ReplaceAll(input, "/d", "")
	input = strings.ReplaceAll(input, "/h", "")
	input = strings.ReplaceAll(input, "/m", "")
	input = strings.ReplaceAll(input, "/s", "")
	input = strings.ReplaceAll(input, "/M", "")
	input = strings.ReplaceAll(input, "/y", "")

	if input == "now" {
		return ref, true
	}

	lower := strings.ToLower(input)
	if strings.HasPrefix(lower, "now") {
		t, ok := parseRelative(input, ref)
		return t, ok
	}

	// Absolute dates.
	layouts := []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, input); err == nil {
			return t, true
		}
	}

	return time.Time{}, false
}

var (
	relativeRE = regexp.MustCompile(`^now\s*([+-])\s*(\d+(?:\.\d+)?)\s*([smhdwMy])$`)
	durationRE = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*([smhdwMy])$`)
)

func parseRelative(input string, ref time.Time) (time.Time, bool) {
	m := relativeRE.FindStringSubmatch(input)
	if m == nil {
		return time.Time{}, false
	}
	sign := m[1]
	amount, _ := strconv.ParseFloat(m[2], 64)
	unit := m[3]

	var d time.Duration
	switch unit {
	case "s":
		d = time.Duration(amount) * time.Second
	case "m":
		d = time.Duration(amount) * time.Minute
	case "h":
		d = time.Duration(amount) * time.Hour
	case "d":
		d = time.Duration(amount*24) * time.Hour
	case "w":
		d = time.Duration(amount*24*7) * time.Hour
	case "M":
		d = time.Duration(amount*30*24) * time.Hour
	case "y":
		d = time.Duration(amount*365*24) * time.Hour
	default:
		return time.Time{}, false
	}

	if sign == "-" {
		return ref.Add(-d), true
	}
	return ref.Add(d), true
}

// Window describes a parsed time interval.
type Window struct {
	Low  time.Time
	High time.Time
}

// Duration returns the interval length. If either bound is missing it returns
// the zero duration and false.
func (w Window) Duration() (time.Duration, bool) {
	if w.Low.IsZero() || w.High.IsZero() {
		return 0, false
	}
	return w.High.Sub(w.Low), true
}

// ParseWindow extracts a time window from range bounds given a reference time.
func ParseWindow(low, high string, ref time.Time) (Window, error) {
	var w Window
	if low != "" && low != "*" {
		t, ok := ParseDateMath(low, ref)
		if !ok {
			return Window{}, fmt.Errorf("cannot parse range low bound %q", low)
		}
		w.Low = t
	}
	if high != "" && high != "*" {
		t, ok := ParseDateMath(high, ref)
		if !ok {
			return Window{}, fmt.Errorf("cannot parse range high bound %q", high)
		}
		w.High = t
	}
	return w, nil
}

// ParseDurationHuman parses strings like "14d", "1M", "2y" into a time.Duration.
func ParseDurationHuman(input string) (time.Duration, bool) {
	m := durationRE.FindStringSubmatch(strings.TrimSpace(input))
	if m == nil {
		return 0, false
	}
	amount, _ := strconv.ParseFloat(m[1], 64)
	unit := m[2]
	switch unit {
	case "s":
		return time.Duration(amount) * time.Second, true
	case "m":
		return time.Duration(amount) * time.Minute, true
	case "h":
		return time.Duration(amount) * time.Hour, true
	case "d":
		return time.Duration(amount*24) * time.Hour, true
	case "w":
		return time.Duration(amount*24*7) * time.Hour, true
	case "M":
		return time.Duration(amount*30*24) * time.Hour, true
	case "y":
		return time.Duration(amount*365*24) * time.Hour, true
	}
	return 0, false
}
