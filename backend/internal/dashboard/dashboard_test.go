package dashboard

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func TestEveryPageLinksTheServedFavicon(t *testing.T) {
	srv := httptest.NewServer(Handler())
	defer srv.Close()

	for _, page := range []string{"index.html", "volumes.html", "text.html", "login.html", "enroll.html", "account.html", "users.html"} {
		body := get(t, srv.URL+"/"+page)
		if !strings.Contains(body.text, `<link rel="icon" href="/favicon.svg" type="image/svg+xml">`) {
			t.Errorf("%s has no favicon link", page)
		}
	}

	icon := get(t, srv.URL+"/favicon.svg")
	if icon.status != http.StatusOK || icon.contentType != "image/svg+xml" {
		t.Errorf("favicon: status %d, type %q", icon.status, icon.contentType)
	}
	if !strings.Contains(icon.text, "🐾") {
		t.Error("favicon should be the brand emoji")
	}
}

type resp struct {
	status      int
	contentType string
	text        string
}

func get(t *testing.T, url string) resp {
	t.Helper()
	r, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := r.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp{r.StatusCode, strings.Split(r.Header.Get("Content-Type"), ";")[0], b.String()}
}

var (
	assetRE     = regexp.MustCompile(`(?:src|href)="(/(?:js|css)/[^"]+)"`)
	scriptTagRE = regexp.MustCompile(`<script(?:\s[^>]*)?>`)
	importRE    = regexp.MustCompile(`from '\./([^']+)'`)
)

// Every page must load only files that exist, keep its scripts external (the
// CSP forbids inline script), and every module it imports must exist too.
func TestPagesLoadOnlyFilesThatExist(t *testing.T) {
	srv := httptest.NewServer(Handler())
	defer srv.Close()

	for _, page := range []string{"index.html", "volumes.html", "text.html", "login.html", "enroll.html", "account.html", "users.html"} {
		body := get(t, srv.URL+"/"+page)
		if body.status != http.StatusOK {
			t.Errorf("%s = %d", page, body.status)
		}
		for _, tag := range scriptTagRE.FindAllString(body.text, -1) {
			if !strings.Contains(tag, " src=") {
				t.Errorf("%s has an inline script, which the CSP blocks: %s", page, tag)
			}
		}
		for _, m := range assetRE.FindAllStringSubmatch(body.text, -1) {
			js := get(t, srv.URL+m[1])
			if js.status != http.StatusOK {
				t.Errorf("%s loads %s = %d", page, m[1], js.status)
			}
			for _, im := range importRE.FindAllStringSubmatch(js.text, -1) {
				if r := get(t, srv.URL+"/js/"+im[1]); r.status != http.StatusOK {
					t.Errorf("%s imports ./%s = %d", m[1], im[1], r.status)
				}
			}
		}
	}
}

// The sign-in and enrollment pages must work signed out, so they may not pull
// in the nav's user menu (which asks who is signed in).
func TestSignedOutPagesDoNotNeedASession(t *testing.T) {
	srv := httptest.NewServer(Handler())
	defer srv.Close()
	for _, js := range []string{"login.js", "enroll.js"} {
		if strings.Contains(get(t, srv.URL+"/js/"+js).text, "user.js") {
			t.Errorf("%s imports user.js, which needs a session", js)
		}
	}
}

func TestPageServesAFileAtAnotherPath(t *testing.T) {
	srv := httptest.NewServer(Page("enroll.html"))
	defer srv.Close()
	r := get(t, srv.URL+"/enroll")
	if r.status != http.StatusOK || !strings.Contains(r.text, "Set up a passkey") || r.contentType != "text/html" {
		t.Fatalf("Page: %d %q", r.status, r.contentType)
	}
	resp, err := http.Get(srv.URL + "/enroll")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Error("an enrollment page needs the strict CSP like every other page")
	}
}
