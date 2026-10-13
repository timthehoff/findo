package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// The list endpoint, not /releases/latest: "latest" skips pre-releases and
	// 404s while every release is one (as all 0.x releases are).
	releasesURL = "https://api.github.com/repos/timthehoff/findo/releases?per_page=30"

	// A failed lookup (offline, rate-limited, no release published yet) is
	// retried sooner than a successful one is refreshed.
	releaseTTL      = 6 * time.Hour
	releaseRetryTTL = 1 * time.Hour
)

type release struct {
	Tag        string `json:"tag_name"`
	URL        string `json:"html_url"`
	Prerelease bool   `json:"prerelease"`
	Draft      bool   `json:"draft"`
}

// updateChecker looks up the latest GitHub release on demand and caches the
// answer, so the dashboard polling /version never turns into GitHub API
// traffic (unauthenticated requests are rate-limited per IP).
type updateChecker struct {
	fetch func(ctx context.Context) (release, error)
	now   func() time.Time

	mu      sync.Mutex
	checked time.Time
	rel     release
	ok      bool
}

// newUpdateChecker offers pre-releases as updates only when includePre is
// set, so stable installs aren't nudged toward release candidates.
func newUpdateChecker(includePre bool) *updateChecker {
	return &updateChecker{
		fetch: func(ctx context.Context) (release, error) { return fetchLatestRelease(ctx, includePre) },
		now:   time.Now,
	}
}

func (u *updateChecker) latest(ctx context.Context) (release, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()

	ttl := releaseRetryTTL
	if u.ok {
		ttl = releaseTTL
	}
	if !u.checked.IsZero() && u.now().Sub(u.checked) < ttl {
		return u.rel, u.ok
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rel, err := u.fetch(ctx)
	u.checked = u.now()
	u.rel, u.ok = rel, err == nil
	return u.rel, u.ok
}

func fetchLatestRelease(ctx context.Context, includePre bool) (release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releasesURL, nil)
	if err != nil {
		return release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "findo-server")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return release{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return release{}, fmt.Errorf("github releases: %s", res.Status)
	}

	var releases []release
	if err := json.NewDecoder(res.Body).Decode(&releases); err != nil {
		return release{}, err
	}
	rel, ok := pickLatest(releases, includePre)
	if !ok {
		return release{}, errors.New("github releases: none published")
	}
	// The URL becomes a link in the dashboard; only ever point it at GitHub.
	if !strings.HasPrefix(rel.URL, "https://github.com/") {
		rel.URL = ""
	}
	return rel, nil
}

// pickLatest returns the highest-versioned published release, skipping
// drafts, pre-releases (unless includePre) and tags that aren't semver.
// Comparing versions, rather than trusting GitHub's list order, keeps a
// backported patch release from masking a newer minor.
func pickLatest(releases []release, includePre bool) (best release, ok bool) {
	var bestV [3]int
	for _, r := range releases {
		if r.Draft || (r.Prerelease && !includePre) {
			continue
		}
		v, valid := parseSemver(r.Tag)
		if !valid || (ok && !semverLess(bestV, v)) {
			continue
		}
		best, bestV, ok = r, v, true
	}
	return best, ok
}

func semverLess(a, b [3]int) bool {
	for i := range a {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// parseSemver reads "v1.2.3" / "1.2.3" (ignoring any -prerelease/+build
// suffix); ok is false for anything else, such as the "dev" default.
func parseSemver(s string) (v [3]int, ok bool) {
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

// isNewer reports whether latest is a strictly higher release than current.
// Unparseable versions (e.g. a "dev" build) are never considered outdated.
func isNewer(latest, current string) bool {
	l, lok := parseSemver(latest)
	c, cok := parseSemver(current)
	if !lok || !cok {
		return false
	}
	return semverLess(c, l)
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	info := struct {
		Current         string `json:"current"`
		Latest          string `json:"latest,omitempty"`
		UpdateAvailable bool   `json:"updateAvailable"`
		URL             string `json:"url,omitempty"`
	}{Current: s.version}

	// A dev build has nothing meaningful to compare, so skip the lookup.
	if _, ok := parseSemver(s.version); ok {
		if rel, ok := s.updates.latest(r.Context()); ok {
			info.Latest = rel.Tag
			info.UpdateAvailable = isNewer(rel.Tag, s.version)
			info.URL = rel.URL
		}
	}
	writeJSON(w, http.StatusOK, info)
}
