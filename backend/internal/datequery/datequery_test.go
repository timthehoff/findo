package datequery

import (
	"testing"
	"time"
)

// Wednesday 2026-10-14 15:30 UTC.
var now = time.Date(2026, time.October, 14, 15, 30, 0, 0, time.UTC)

func ts(y int, m time.Month, d int) int64 {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).Unix()
}

func TestParse(t *testing.T) {
	cases := []struct {
		in         string
		rest       string
		from, to   int64
		wantPhrase string
	}{
		{"budget", "budget", 0, 0, ""},
		{"today", "", ts(2026, 10, 14), ts(2026, 10, 15), "today"},
		{"report yesterday", "report", ts(2026, 10, 13), ts(2026, 10, 14), "yesterday"},
		{"tax from the last month", "tax", now.AddDate(0, -1, 0).Unix(), 0, "from the last month"},
		{"last week photos", "photos", now.AddDate(0, 0, -7).Unix(), 0, "last week"},
		{"Past 3 Days invoice", "invoice", now.AddDate(0, 0, -3).Unix(), 0, "Past 3 Days"},
		{"last 2 years", "", now.AddDate(-2, 0, 0).Unix(), 0, "last 2 years"},
		{"this week", "", ts(2026, 10, 12), 0, "this week"},
		{"notes this month", "notes", ts(2026, 10, 1), 0, "this month"},
		{"this year", "", ts(2026, 1, 1), 0, "this year"},
		{"taxes in march 2024", "taxes", ts(2024, 3, 1), ts(2024, 4, 1), "in march 2024"},
		{"taxes in march", "taxes", ts(2026, 3, 1), ts(2026, 4, 1), "in march"},
		{"scans from december", "scans", ts(2025, 12, 1), ts(2026, 1, 1), "from december"},
		{"since 2023", "", ts(2023, 1, 1), 0, "since 2023"},
		{"before 2020 tax", "tax", 0, ts(2020, 1, 1), "before 2020"},
		{"after june 2025", "", ts(2025, 7, 1), 0, "after june 2025"},
		{"until 2024", "", 0, ts(2025, 1, 1), "until 2024"},
		{"in 2022", "", ts(2022, 1, 1), ts(2023, 1, 1), "in 2022"},
		{"on 2024-02-29 receipt", "receipt", ts(2024, 2, 29), ts(2024, 3, 1), "on 2024-02-29"},
		{"since sept 2025", "", ts(2025, 9, 1), 0, "since sept 2025"},

		// Things that look temporal but are just filenames.
		{"invoice 2023", "invoice 2023", 0, 0, ""},
		{"may", "may", 0, 0, ""},
		{"march madness", "march madness", 0, 0, ""},
		{"lastweek", "lastweek", 0, 0, ""},
		{"on 2024-13-45", "on 2024-13-45", 0, 0, ""},
		{"last 0 days", "last 0 days", 0, 0, ""},
		{"in 1850", "in 1850", 0, 0, ""},
	}
	for _, c := range cases {
		rest, r, phrase := Parse(c.in, now)
		if rest != c.rest || r.From != c.from || r.To != c.to || phrase != c.wantPhrase {
			t.Errorf("Parse(%q) = (%q, {%d %d}, %q), want (%q, {%d %d}, %q)",
				c.in, rest, r.From, r.To, phrase, c.rest, c.from, c.to, c.wantPhrase)
		}
	}
}

func TestParseUsesLocation(t *testing.T) {
	la, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	local := time.Date(2026, time.October, 14, 1, 0, 0, 0, la) // 08:00 UTC, so UTC midnight would be wrong
	_, r, _ := Parse("today", local)
	want := time.Date(2026, time.October, 14, 0, 0, 0, 0, la).Unix()
	if r.From != want {
		t.Fatalf("today.From = %d, want %d (local midnight)", r.From, want)
	}
}

func TestParseMonthAheadRollsBackAYear(t *testing.T) {
	// November hasn't happened yet in October 2026, so "in november" is last year's.
	_, r, _ := Parse("in november", now)
	if r.From != ts(2025, 11, 1) || r.To != ts(2025, 12, 1) {
		t.Fatalf("got {%d %d}", r.From, r.To)
	}
}

func TestRangeIsZero(t *testing.T) {
	if !(Range{}).IsZero() || (Range{From: 1}).IsZero() || (Range{To: 1}).IsZero() {
		t.Fatal("IsZero wrong")
	}
}
