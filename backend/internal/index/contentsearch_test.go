package index

import (
	"sort"
	"strings"
	"testing"
)

func TestTSQuery(t *testing.T) {
	tests := []struct{ in, want string }{
		{"budget", "'budget':*"},
		{"Quarterly Budget", "'quarterly' & 'budget':*"},
		{"  spaced   out  ", "'spaced' & 'out':*"},
		{"report 2024", "'report' & '2024':*"},
		{"café menu", "'café' & 'menu':*"},
		{"日本語 テスト", "'日本語' & 'テスト':*"},
		// A one-letter last word isn't a prefix, but the query still has a real word.
		{"budget q", "'budget' & 'q'"},
		// Operators, quotes and punctuation are just separators.
		{"foo & bar | !baz", "'foo' & 'bar' & 'baz':*"},
		{"it's (a) <-> test:*", "'it' & 's' & 'a' & 'test':*"},
		{"'; DROP TABLE files; --", "'drop' & 'table' & 'files':*"},
		{"NEAR/3 the", "'near' & '3' & 'the':*"},
		// Nothing searchable.
		{"", ""},
		{"   ", ""},
		{"!!! ???", ""},
		{"a", ""},
		{"a b c", ""},
	}
	for _, tt := range tests {
		if got := tsQuery(tt.in); got != tt.want {
			t.Errorf("tsQuery(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
	if got := tsQuery(strings.Repeat("word ", 40)); strings.Count(got, "&") != maxQueryTokens-1 {
		t.Errorf("a long query should be capped at %d terms: %q", maxQueryTokens, got)
	}
}

// indexed seeds a file with extracted chunks.
func indexed(t *testing.T, idx *Index, vol int64, path string, mod int64, chunks ...string) {
	t.Helper()
	seedFiles(t, idx, vol, File{Path: path, Size: 10, ModTime: mod})
	if err := idx.SaveContent(vol, path, 10, mod, chunks); err != nil {
		t.Fatal(err)
	}
}

func contentPaths(t *testing.T, idx *Index, vol int64, q string, hidden bool, r TimeRange) []string {
	t.Helper()
	got, err := idx.SearchContent(vol, q, 50, hidden, r)
	if err != nil {
		t.Fatalf("SearchContent(%q): %v", q, err)
	}
	var out []string
	for _, m := range got {
		out = append(out, m.Path)
	}
	return out
}

func TestSearchContentMatchingRules(t *testing.T) {
	idx := openTest(t)
	vol := optedInVolume(t, idx, "nas")
	indexed(t, idx, vol, "minutes.txt", 1, "The committee approved the quarterly budgets on Tuesday.")
	indexed(t, idx, vol, "recipe.md", 1, "Whisk the eggs, then fold in the flour.")

	for q, want := range map[string][]string{
		"budget":           {"minutes.txt"}, // stemming: "budgets" in the text
		"budgets":          {"minutes.txt"},
		"quarterly budget": {"minutes.txt"}, // every word must match
		"quarterly flour":  nil,
		"committee appro":  {"minutes.txt"}, // last word is a prefix
		"APPROVED":         {"minutes.txt"}, // case-insensitive
		"eggs":             {"recipe.md"},
		"nonexistent":      nil,
		"the":              nil, // a stop word alone matches nothing
		"x":                nil, // too short to search
		"":                 nil,
	} {
		if got := contentPaths(t, idx, 0, q, true, TimeRange{}); !equalStrings(got, want) {
			t.Errorf("SearchContent(%q) = %v, want %v", q, got, want)
		}
	}

	// Hostile input must be harmless, not an error.
	for _, q := range []string{"'; DROP TABLE files; --", "a & | b", "!!!", "( ) : *", "\\", "'''", "foo:* & bar:*"} {
		if _, err := idx.SearchContent(0, q, 10, true, TimeRange{}); err != nil {
			t.Errorf("SearchContent(%q) failed: %v", q, err)
		}
	}
}

func TestSearchContentSnippetHighlightsTheMatch(t *testing.T) {
	idx := openTest(t)
	vol := optedInVolume(t, idx, "nas")
	long := strings.Repeat("Filler sentence about nothing in particular. ", 20) +
		"The migration plan was finalised on Friday. " +
		strings.Repeat("More unrelated filler text goes here. ", 20)
	indexed(t, idx, vol, "plan.txt", 1, long)

	got, err := idx.SearchContent(0, "migration plan", 10, true, TimeRange{})
	if err != nil || len(got) != 1 {
		t.Fatalf("got %+v, %v", got, err)
	}
	sn := got[0].Snippet
	if !strings.Contains(sn, MarkStart+"migration"+MarkEnd) || !strings.Contains(sn, MarkStart+"plan"+MarkEnd) {
		t.Fatalf("snippet doesn't highlight the matched words: %q", sn)
	}
	if len(sn) > 400 {
		t.Fatalf("snippet should be a short passage, got %d bytes", len(sn))
	}
	if strings.Count(sn, MarkStart) != strings.Count(sn, MarkEnd) {
		t.Fatalf("unbalanced markers: %q", sn)
	}
}

func TestSearchContentPicksTheBestChunkPerFile(t *testing.T) {
	idx := openTest(t)
	vol := optedInVolume(t, idx, "nas")
	indexed(t, idx, vol, "report.txt", 1,
		"Intro mentioning invoice once among many other unrelated words.",
		"invoice invoice invoice: the invoice total for this invoice is due.",
		"Closing remarks.")
	indexed(t, idx, vol, "other.txt", 1, "A single invoice appears here.")

	got, err := idx.SearchContent(0, "invoice", 10, true, TimeRange{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want one result per file, got %d: %+v", len(got), got)
	}
	if got[0].Path != "report.txt" {
		t.Fatalf("the file with the stronger match should rank first, got %s", got[0].Path)
	}
	if !strings.Contains(got[0].Snippet, "total") {
		t.Fatalf("report.txt should show its best chunk, got %q", got[0].Snippet)
	}
}

func TestSearchContentLeavesOutNameMatchesAndAppliesFilters(t *testing.T) {
	idx := openTest(t)
	a, b := optedInVolume(t, idx, "a"), optedInVolume(t, idx, "b")
	indexed(t, idx, a, "invoice-2024.txt", 1000, "This invoice file also mentions the invoice in its text.")
	indexed(t, idx, a, "notes.txt", 2000, "meeting notes: send the invoice on Monday")
	indexed(t, idx, a, ".private/draft.txt", 3000, "hidden invoice draft")
	indexed(t, idx, b, "ledger.txt", 4000, "invoice ledger entry")

	// The file named "invoice..." is the name search's job.
	all := contentPaths(t, idx, 0, "invoice", true, TimeRange{})
	sort.Strings(all)
	if !equalStrings(all, []string{".private/draft.txt", "ledger.txt", "notes.txt"}) {
		t.Fatalf("all volumes, hidden shown (and invoice-2024.txt left to name search): %v", all)
	}
	if got := contentPaths(t, idx, 0, "invoice", false, TimeRange{}); len(got) != 2 {
		t.Fatalf("hidden files must be excluded by default: %v", got)
	}
	if got := contentPaths(t, idx, b, "invoice", true, TimeRange{}); !equalStrings(got, []string{"ledger.txt"}) {
		t.Fatalf("volume filter: %v", got)
	}
	if got := contentPaths(t, idx, 0, "invoice", true, TimeRange{From: 1500, To: 2500}); !equalStrings(got, []string{"notes.txt"}) {
		t.Fatalf("modified range: %v", got)
	}
}

func TestSearchContentIgnoresPurgedText(t *testing.T) {
	idx := openTest(t)
	vol := optedInVolume(t, idx, "nas")
	indexed(t, idx, vol, "a.txt", 1, "searchable secret phrase")
	if got := contentPaths(t, idx, 0, "secret", true, TimeRange{}); len(got) != 1 {
		t.Fatalf("before opting out: %v", got)
	}
	if _, err := idx.SetVolumeContentIndex(vol, false); err != nil {
		t.Fatal(err)
	}
	if got := contentPaths(t, idx, 0, "secret", true, TimeRange{}); len(got) != 0 {
		t.Fatalf("after opting out the text must not be searchable: %v", got)
	}
}

func TestSearchContentLimit(t *testing.T) {
	idx := openTest(t)
	vol := optedInVolume(t, idx, "nas")
	for _, p := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		indexed(t, idx, vol, p, 1, "shared keyword content")
	}
	got, err := idx.SearchContent(0, "keyword", 2, true, TimeRange{})
	if err != nil || len(got) != 2 {
		t.Fatalf("limit 2 returned %d, %v", len(got), err)
	}
}
