package index

import (
	"strconv"
	"strings"
	"unicode"
)

// Highlight markers in snippets: control characters that can't appear in
// extracted text (the extractor strips them), so the browser can HTML-escape
// a snippet and then turn the markers into <mark> tags without trusting it.
const (
	MarkStart = "\x02"
	MarkEnd   = "\x03"
)

const (
	maxQueryTokens = 16
	minTermLen     = 2 // shorter words match too much to be worth searching
)

// tsQuery turns what a person typed into a Postgres tsquery: every word must
// match (AND), and the last word also matches as a prefix, so results appear
// while typing. Only letters and digits survive, so operators, quotes and
// punctuation in the input can't change the query's meaning or break it. It
// returns "" when there's nothing worth searching for.
func tsQuery(q string) string {
	tokens := strings.FieldsFunc(strings.ToLower(q), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(tokens) > maxQueryTokens {
		tokens = tokens[:maxQueryTokens]
	}
	long := false
	for _, t := range tokens {
		if len([]rune(t)) >= minTermLen {
			long = true
		}
	}
	if !long {
		return ""
	}

	parts := make([]string, len(tokens))
	for i, t := range tokens {
		parts[i] = "'" + t + "'"
		if i == len(tokens)-1 && len([]rune(t)) >= minTermLen {
			parts[i] += ":*"
		}
	}
	return strings.Join(parts, " & ")
}

// ContentMatch is a file whose extracted text matched, with the best-matching
// passage (highlight markers around the matched words).
type ContentMatch struct {
	File
	Snippet string
}

// SearchContent finds files whose extracted text matches q, best match first.
// Each file appears once, with the passage that ranked highest. Files whose
// name already matches q are left out (name search returns those), and the
// volume, hidden-file and modified-time filters work as in Search.
func (idx *Index) SearchContent(volumeID int64, q string, limit int, includeHidden bool, modified TimeRange) ([]ContentMatch, error) {
	tq := tsQuery(q)
	if tq == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}

	args := []interface{}{tq}
	arg := func(v interface{}) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}

	where := []string{`c.tsv @@ q.tq`, `NOT (f.name ILIKE ` + arg("%"+likeEscape(q)+"%") + `)`}
	if volumeID != 0 {
		where = append(where, `f.volume_id = `+arg(volumeID))
	}
	if modified.From != 0 {
		where = append(where, `f.mod_time >= `+arg(modified.From))
	}
	if modified.To != 0 {
		where = append(where, `f.mod_time < `+arg(modified.To))
	}
	if !includeHidden {
		where = append(where, `f.path NOT LIKE '.%' AND f.path NOT LIKE '%/.%'`)
	}
	headline := arg("StartSel=" + MarkStart + ",StopSel=" + MarkEnd + ",MaxFragments=1,MinWords=12,MaxWords=32,ShortWord=1")
	limitArg := arg(limit)

	// Rank and pick the best chunk per file first; only the final handful of
	// rows pay for ts_headline, which is the expensive part.
	rows, err := idx.db.Query(`
		WITH q AS (SELECT to_tsquery('english', $1) AS tq),
		best AS (
			SELECT DISTINCT ON (c.volume_id, c.path)
			       c.id AS chunk_id, c.volume_id, c.path, ts_rank_cd(c.tsv, q.tq) AS rank
			FROM q, chunks c
			JOIN files f ON f.volume_id = c.volume_id AND f.path = c.path
			WHERE `+strings.Join(where, ` AND `)+`
			ORDER BY c.volume_id, c.path, rank DESC
		),
		top AS (
			SELECT * FROM best ORDER BY rank DESC, volume_id, path COLLATE "C" LIMIT `+limitArg+`
		)
		SELECT f.volume_id, f.path, f.name, f.dir, f.ext, f.is_dir, f.size, f.mod_time,
		       ts_headline('english', c.body, q.tq, `+headline+`)
		FROM top t
		JOIN chunks c ON c.id = t.chunk_id
		JOIN files f ON f.volume_id = t.volume_id AND f.path = t.path
		CROSS JOIN q
		ORDER BY t.rank DESC, f.volume_id, f.path COLLATE "C"`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ContentMatch
	for rows.Next() {
		var m ContentMatch
		if err := rows.Scan(&m.VolumeID, &m.Path, &m.Name, &m.Dir, &m.Ext, &m.IsDir, &m.Size, &m.ModTime, &m.Snippet); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
