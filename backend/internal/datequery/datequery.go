// Package datequery pulls a fuzzy time phrase ("last month", "in March 2024",
// "since 2023") out of a search query and turns it into a modification-time
// range, leaving the rest of the query for name matching.
//
// It is deliberately small and hand-rolled: file search only looks backwards,
// and callers need a [From, To) range, not the single instant most general
// date parsers return. Years and months only count after a keyword (in, from,
// since, ...) so a filename search like "invoice 2023" or "may" is untouched.
package datequery

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Range is a half-open [From, To) span of unix seconds. Zero means unbounded
// on that side.
type Range struct {
	From int64 `json:"from,omitempty"`
	To   int64 `json:"to,omitempty"`
}

// IsZero reports whether the range constrains nothing.
func (r Range) IsZero() bool { return r.From == 0 && r.To == 0 }

var months = map[string]time.Month{
	"jan": time.January, "january": time.January,
	"feb": time.February, "february": time.February,
	"mar": time.March, "march": time.March,
	"apr": time.April, "april": time.April,
	"may": time.May,
	"jun": time.June, "june": time.June,
	"jul": time.July, "july": time.July,
	"aug": time.August, "august": time.August,
	"sep": time.September, "sept": time.September, "september": time.September,
	"oct": time.October, "october": time.October,
	"nov": time.November, "november": time.November,
	"dec": time.December, "december": time.December,
}

const (
	// Optional filler so "files from the last month" strips cleanly.
	relPrefix = `\b(?:(?:from|in|within|during|over)\s+)?(?:the\s+)?`
	monthName = `jan(?:uary)?|feb(?:ruary)?|mar(?:ch)?|apr(?:il)?|may|june?|july?|aug(?:ust)?|sept?(?:ember)?|oct(?:ober)?|nov(?:ember)?|dec(?:ember)?`
	year      = `(?:19[7-9]\d|20\d\d)`
)

var (
	dayRe      = regexp.MustCompile(`(?i)` + relPrefix + `\b(today|yesterday)\b`)
	trailingRe = regexp.MustCompile(`(?i)` + relPrefix + `(?:last|past)\s+(?:(\d{1,3})\s+)?(day|week|month|year)s?\b`)
	thisRe     = regexp.MustCompile(`(?i)\b(?:(?:from|in|during)\s+)?this\s+(week|month|year)\b`)
	// keyword + (month year | month | ISO date | year); alternation order
	// matters so "in may 2024" doesn't stop at "in may".
	periodRe = regexp.MustCompile(`(?i)\b(in|during|from|on|since|after|before|until)\s+(?:(` + monthName + `)\s+(` + year + `)|(` + monthName + `)|(\d{4}-\d{2}-\d{2})|(` + year + `))\b`)
)

// Parse extracts the first recognised time phrase from q. rest is q with the
// phrase removed (whitespace collapsed); phrase is the text that was removed.
// A zero Range means nothing was recognised and rest == q. now's location
// decides where days, weeks and months begin.
func Parse(q string, now time.Time) (rest string, r Range, phrase string) {
	for _, try := range []func(string, time.Time) (Range, []int){parseDay, parseTrailing, parseThis, parsePeriod} {
		if rng, loc := try(q, now); loc != nil {
			phrase = q[loc[0]:loc[1]]
			rest = strings.Join(strings.Fields(q[:loc[0]]+" "+q[loc[1]:]), " ")
			return rest, rng, strings.TrimSpace(phrase)
		}
	}
	return q, Range{}, ""
}

func parseDay(q string, now time.Time) (Range, []int) {
	m := dayRe.FindStringSubmatchIndex(q)
	if m == nil {
		return Range{}, nil
	}
	start := startOfDay(now)
	if strings.EqualFold(q[m[2]:m[3]], "yesterday") {
		start = start.AddDate(0, 0, -1)
	}
	return Range{From: start.Unix(), To: start.AddDate(0, 0, 1).Unix()}, m
}

// "last/past N units" is a trailing window ending now, which is what people
// mean by "from the last month" — not the previous calendar month.
func parseTrailing(q string, now time.Time) (Range, []int) {
	m := trailingRe.FindStringSubmatchIndex(q)
	if m == nil {
		return Range{}, nil
	}
	n := 1
	if m[2] >= 0 {
		n, _ = strconv.Atoi(q[m[2]:m[3]])
	}
	if n < 1 {
		return Range{}, nil
	}
	var from time.Time
	switch strings.ToLower(q[m[4]:m[5]]) {
	case "day":
		from = now.AddDate(0, 0, -n)
	case "week":
		from = now.AddDate(0, 0, -7*n)
	case "month":
		from = now.AddDate(0, -n, 0)
	default:
		from = now.AddDate(-n, 0, 0)
	}
	return Range{From: from.Unix()}, m
}

// "this week/month/year" is calendar-to-date (weeks start Monday).
func parseThis(q string, now time.Time) (Range, []int) {
	m := thisRe.FindStringSubmatchIndex(q)
	if m == nil {
		return Range{}, nil
	}
	day := startOfDay(now)
	var from time.Time
	switch strings.ToLower(q[m[2]:m[3]]) {
	case "week":
		from = day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7))
	case "month":
		from = time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, day.Location())
	default:
		from = time.Date(day.Year(), time.January, 1, 0, 0, 0, 0, day.Location())
	}
	return Range{From: from.Unix()}, m
}

func parsePeriod(q string, now time.Time) (Range, []int) {
	m := periodRe.FindStringSubmatchIndex(q)
	if m == nil {
		return Range{}, nil
	}
	group := func(i int) string {
		if m[2*i] < 0 {
			return ""
		}
		return q[m[2*i]:m[2*i+1]]
	}
	loc := now.Location()
	var ps, pe time.Time
	switch {
	case group(2) != "": // month + year
		ps = time.Date(atoi(group(3)), months[strings.ToLower(group(2))], 1, 0, 0, 0, 0, loc)
		pe = ps.AddDate(0, 1, 0)
	case group(4) != "": // month alone: its most recent occurrence not in the future
		mo := months[strings.ToLower(group(4))]
		ps = time.Date(now.Year(), mo, 1, 0, 0, 0, 0, loc)
		if ps.After(now) {
			ps = ps.AddDate(-1, 0, 0)
		}
		pe = ps.AddDate(0, 1, 0)
	case group(5) != "": // ISO date
		d, err := time.ParseInLocation("2006-01-02", group(5), loc)
		if err != nil {
			return Range{}, nil
		}
		ps, pe = d, d.AddDate(0, 0, 1)
	default: // year
		ps = time.Date(atoi(group(6)), time.January, 1, 0, 0, 0, 0, loc)
		pe = ps.AddDate(1, 0, 0)
	}

	var r Range
	switch strings.ToLower(group(1)) {
	case "since":
		r.From = ps.Unix()
	case "after":
		r.From = pe.Unix()
	case "before":
		r.To = ps.Unix()
	case "until":
		r.To = pe.Unix()
	default: // in, during, from, on
		r = Range{From: ps.Unix(), To: pe.Unix()}
	}
	return r, m
}

func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}
