package sms_test

import (
	"strings"
	"testing"

	"github.com/1995parham-me/sms2telegram/internal/domain/sms"
)

func TestMeasure(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		text string
		want sms.Size
	}{
		"short ascii":     {"on my way", sms.Size{Encoding: sms.GSM7, Units: 9, Parts: 1}},
		"exactly one":     {strings.Repeat("a", 160), sms.Size{Encoding: sms.GSM7, Units: 160, Parts: 1}},
		"one over":        {strings.Repeat("a", 161), sms.Size{Encoding: sms.GSM7, Units: 161, Parts: 2}},
		"extended":        {"[€]", sms.Size{Encoding: sms.GSM7, Units: 6, Parts: 1}},
		"persian":         {"سلام", sms.Size{Encoding: sms.UCS2, Units: 4, Parts: 1}},
		"persian limit":   {strings.Repeat("س", 70), sms.Size{Encoding: sms.UCS2, Units: 70, Parts: 1}},
		"persian over":    {strings.Repeat("س", 71), sms.Size{Encoding: sms.UCS2, Units: 71, Parts: 2}},
		"emoji is a pair": {"ok 🌹", sms.Size{Encoding: sms.UCS2, Units: 5, Parts: 1}},
		"empty":           {"", sms.Size{Encoding: sms.GSM7, Units: 0, Parts: 1}},
	} {
		if got := sms.Measure(tc.text); got != tc.want {
			t.Errorf("%s: got %+v, want %+v", name, got, tc.want)
		}
	}
}
