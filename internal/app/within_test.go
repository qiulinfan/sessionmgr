package app

import (
	"testing"
	"time"
)

func TestParseWithin(t *testing.T) {
	for input, want := range map[string]time.Duration{
		"30d": 30 * 24 * time.Hour, "7D": 7 * 24 * time.Hour, " 12h ": 12 * time.Hour,
		"90m": 90 * time.Minute, "all": 0, "0": 0,
	} {
		got, err := parseWithin(input)
		if err != nil || got != want {
			t.Errorf("parseWithin(%q) = %v, %v; want %v", input, got, err, want)
		}
	}
	for _, input := range []string{"", "0d", "-3d", "1.5d", "week", "-1h"} {
		if _, err := parseWithin(input); err == nil {
			t.Errorf("parseWithin(%q) accepted an invalid period", input)
		}
	}
}
