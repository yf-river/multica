package handler

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestSinceFromDays(t *testing.T) {
	mustLoad := func(name string) *time.Location {
		loc, err := time.LoadLocation(name)
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		return loc
	}
	ny := mustLoad("America/New_York")
	la := mustLoad("America/Los_Angeles")

	cases := []struct {
		name    string
		loc     *time.Location
		now     time.Time
		days    int
		wantYMD [3]int
	}{
		{
			name:    "NY spring-forward span",
			loc:     ny,
			now:     time.Date(2025, 3, 12, 15, 30, 0, 0, ny),
			days:    3,
			wantYMD: [3]int{2025, 3, 9},
		},
		{
			name:    "NY spring-forward day itself",
			loc:     ny,
			now:     time.Date(2025, 3, 9, 10, 0, 0, 0, ny),
			days:    0,
			wantYMD: [3]int{2025, 3, 9},
		},
		{
			name:    "NY fall-back span",
			loc:     ny,
			now:     time.Date(2025, 11, 5, 8, 0, 0, 0, ny),
			days:    4,
			wantYMD: [3]int{2025, 11, 1},
		},
		{
			name:    "NY fall-back day itself",
			loc:     ny,
			now:     time.Date(2025, 11, 2, 23, 59, 0, 0, ny),
			days:    0,
			wantYMD: [3]int{2025, 11, 2},
		},
		{
			name:    "LA spring-forward span",
			loc:     la,
			now:     time.Date(2025, 3, 10, 6, 15, 0, 0, la),
			days:    2,
			wantYMD: [3]int{2025, 3, 8},
		},
		{
			name:    "LA fall-back span",
			loc:     la,
			now:     time.Date(2025, 11, 3, 0, 30, 0, 0, la),
			days:    5,
			wantYMD: [3]int{2025, 10, 29},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sinceFromDays(tc.now, tc.days, tc.loc)

			y, m, d := got.In(tc.loc).Date()
			hh, mm, ss := got.In(tc.loc).Clock()
			if hh != 0 || mm != 0 || ss != 0 {
				t.Errorf("not local midnight: got %s in %s", got.In(tc.loc).Format(time.RFC3339), tc.loc)
			}
			if y != tc.wantYMD[0] || int(m) != tc.wantYMD[1] || d != tc.wantYMD[2] {
				t.Errorf("calendar day: got %04d-%02d-%02d, want %04d-%02d-%02d",
					y, m, d, tc.wantYMD[0], tc.wantYMD[1], tc.wantYMD[2])
			}

			want := time.Date(tc.wantYMD[0], time.Month(tc.wantYMD[1]), tc.wantYMD[2], 0, 0, 0, 0, tc.loc)
			if !got.Equal(want) {
				t.Errorf("instant mismatch: got %s, want %s",
					got.Format(time.RFC3339), want.Format(time.RFC3339))
			}
		})
	}
}

func TestParseSinceParamInTZ(t *testing.T) {
	utc := time.UTC

	expectDays := func(days int) time.Time {
		return sinceFromDays(time.Now(), days, utc)
	}

	cases := []struct {
		name        string
		query       string
		defaultDays int
		wantDays    int
	}{
		{"days=0 rejected (not > 0)", "days=0", 30, 30},
		{"days=abc rejected (not an int)", "days=abc", 30, 30},
		{"days=400 rejected (over 365 cap)", "days=400", 30, 30},
		{"days=-5 rejected (negative)", "days=-5", 30, 30},
		{"no days param uses default", "", 30, 30},
		{"empty days param uses default", "days=", 30, 30},
		{"valid days=7 honoured", "days=7", 30, 7},
		{"days=365 honoured (at cap)", "days=365", 30, 365},
		{"days=1 honoured (lower bound)", "days=1", 90, 1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			url := "/api/dashboard/usage/daily"
			if tc.query != "" {
				url += "?" + tc.query
			}
			req := httptest.NewRequest("GET", url, nil)

			got := parseSinceParamInTZ(req, tc.defaultDays, "UTC")
			if !got.Valid {
				t.Fatalf("expected a valid timestamptz, got Valid=false")
			}
			want := expectDays(tc.wantDays)
			if diff := got.Time.Sub(want); diff < -2*time.Second || diff > 2*time.Second {
				t.Errorf("cutoff mismatch: got %s, want ~%s (effective days=%d)",
					got.Time.Format(time.RFC3339), want.Format(time.RFC3339), tc.wantDays)
			}
		})
	}
}
