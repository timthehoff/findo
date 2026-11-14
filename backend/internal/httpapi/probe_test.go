package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hirochachacha/go-smb2"

	"github.com/thoff/findo/backend/internal/index"
	"github.com/thoff/findo/backend/internal/smbclient"
)

// failingNAS makes newSMB hand out a NAS whose Connect fails with the error
// registered for the host.
func failingNAS(srv *Server, byHost map[string]error) {
	srv.newSMB = func(host, share, user, pass string) smbConnector {
		f := newFakeSMB()
		f.connectErr = byHost[host]
		return f
	}
}

var hostErrors = map[string]error{
	"refused.lan": fmt.Errorf("dial 10.0.0.7:445: %w", &net.OpError{Op: "dial", Err: errors.New("connect: connection refused")}),
	"silent.lan":  fmt.Errorf("dial 10.0.0.8:445: %w", &net.OpError{Op: "dial", Err: errors.New("i/o timeout")}),
	"http.lan":    fmt.Errorf("smb session setup: %w", errors.New("unexpected EOF")),
	"badpw.lan":   fmt.Errorf("smb session setup: %w", &smb2.ResponseError{Code: 0xC000006D}),
	"noshare.lan": fmt.Errorf("mount share: %w", &smb2.ResponseError{Code: 0xC00000CC}),
}

func (w *world) testInput(c *http.Cookie, host string) (string, int) {
	t := w.t
	rec := w.req(c, "POST", "/volumes/test", map[string]string{"host": host, "share": "s", "username": "u", "password": "p"})
	var out struct {
		OK    bool
		Error string
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	_ = t
	return out.Error, rec.Code
}

func TestConnectionTestsDoNotRevealWhyAHostIsUnreachable(t *testing.T) {
	w := newWorld(t)
	failingNAS(w.srv, hostErrors)

	generic := "Couldn't connect to an SMB server at that address."
	for _, host := range []string{"refused.lan", "silent.lan", "http.lan"} {
		msg, code := w.testInput(w.bobC, host)
		if code != http.StatusOK || msg != generic {
			t.Errorf("%s: %d %q, want the one generic message", host, code, msg)
		}
	}
	// Once an SMB server answers, saying so is the point of the test.
	for host, want := range map[string]string{
		"badpw.lan":   "The server refused that username or password.",
		"noshare.lan": "The server has no share with that name.",
	} {
		if msg, _ := w.testInput(w.bobC, host); msg != want {
			t.Errorf("%s: %q, want %q", host, msg, want)
		}
	}
	// Saved volumes report the same way, and what is stored is limited too.
	w.srv.newSMB = func(host, share, user, pass string) smbConnector {
		f := newFakeSMB()
		f.connectErr = hostErrors["refused.lan"]
		return f
	}
	rec := w.req(w.bobC, "POST", "/volumes/"+w.itoa(w.bobVol.ID)+"/test", nil)
	if strings.Contains(rec.Body.String(), "connection refused") || !strings.Contains(rec.Body.String(), generic) {
		t.Errorf("saved volume test: %s", rec.Body)
	}
	if v, _, _ := w.srv.idx.GetVolume(w.bobVol.ID); v.LastTestError != generic {
		t.Errorf("stored test error %q, want the generic message", v.LastTestError)
	}
}

func TestAFailedConnectToANewVolumeIsStoredWithoutDetail(t *testing.T) {
	w := newWorld(t)
	failingNAS(w.srv, map[string]error{"refused.lan": hostErrors["refused.lan"]})
	rec := w.req(w.bobC, "POST", "/volumes", map[string]string{"name": "probe", "host": "refused.lan", "share": "s", "username": "u", "password": "p"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	var vol index.Volume
	decode(t, rec.Body.Bytes(), &vol)
	waitFor(t, func() bool {
		v, _, _ := w.srv.idx.GetVolume(vol.ID)
		return v.LastTestError != ""
	})
	v, _, _ := w.srv.idx.GetVolume(vol.ID)
	if v.LastTestError != "Couldn't connect to an SMB server at that address." {
		t.Fatalf("stored error %q", v.LastTestError)
	}
	// What the person sees in the list is the same.
	if body := w.req(w.bobC, "GET", "/volumes", nil).Body.String(); strings.Contains(body, "connection refused") || strings.Contains(body, "10.0.0.7") {
		t.Errorf("the volume list leaks the raw error: %s", body)
	}
}

func TestLocalStartupProblemsKeepTheirOwnMessage(t *testing.T) {
	w := newWorld(t)
	annSecret, _ := w.srv.idx.GetVolumeSecret(w.annVol.ID)
	w.srv.idx.SetVolumeOwner(w.bobVol.ID, w.bob.ID, func([]byte) ([]byte, error) { return annSecret.PasswordEnc, nil })
	vol, _, _ := w.srv.idx.GetVolume(w.bobVol.ID)
	err := w.srv.StartVolume(context.Background(), vol)
	var cf *connectFailure
	if err == nil || errors.As(err, &cf) || !strings.Contains(err.Error(), "decrypt") {
		t.Fatalf("a decrypt failure is local and must not be reported as a connection failure: %v", err)
	}
}

func TestFailuresToReachTakeAsLongAsTheTimeoutButAnswersDoNot(t *testing.T) {
	old := smbclient.ConnectTimeout
	smbclient.ConnectTimeout = 400 * time.Millisecond
	t.Cleanup(func() { smbclient.ConnectTimeout = old })

	w := newWorld(t)
	failingNAS(w.srv, hostErrors)
	timed := func(host string) time.Duration {
		start := time.Now()
		w.testInput(w.bobC, host)
		return time.Since(start)
	}
	for _, host := range []string{"refused.lan", "silent.lan", "http.lan"} {
		if d := timed(host); d < 380*time.Millisecond {
			t.Errorf("%s answered after %v: a refusal must take as long as a timeout (%v)", host, d, smbclient.ConnectTimeout)
		}
	}
	if d := timed("badpw.lan"); d > 300*time.Millisecond {
		t.Errorf("a server that answered shouldn't be slowed: %v", d)
	}
}

func TestEachUserGetsAFewConnectionAttemptsAMinute(t *testing.T) {
	w := newWorld(t)
	failingNAS(w.srv, nil)

	for i := 0; i < connectsPerMinute; i++ {
		if _, code := w.testInput(w.bobC, "h"); code != http.StatusOK {
			t.Fatalf("attempt %d = %d", i+1, code)
		}
	}
	rec := w.req(w.bobC, "POST", "/volumes/test", map[string]string{"host": "h", "share": "s", "username": "u", "password": "p"})
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("one over the limit = %d %s (Retry-After %q)", rec.Code, rec.Body, rec.Header().Get("Retry-After"))
	}
	// The budget is shared by everything that connects: saved tests, creating
	// and saving volumes, and none of those happen once it's spent.
	for name, call := range map[string]func() *httptest.ResponseRecorder{
		"test a saved volume": func() *httptest.ResponseRecorder {
			return w.req(w.bobC, "POST", "/volumes/"+w.itoa(w.bobVol.ID)+"/test", nil)
		},
		"create a volume": func() *httptest.ResponseRecorder {
			return w.req(w.bobC, "POST", "/volumes", map[string]string{"name": "more", "host": "h", "share": "s", "username": "u", "password": "p"})
		},
		"save a volume": func() *httptest.ResponseRecorder {
			return w.req(w.bobC, "PUT", "/volumes/"+w.itoa(w.bobVol.ID), map[string]string{"name": "renamed", "host": w.bobVol.Host, "share": w.bobVol.Share, "username": w.bobVol.Username})
		},
	} {
		if rec := call(); rec.Code != http.StatusTooManyRequests {
			t.Errorf("%s past the limit = %d %s, want 429", name, rec.Code, rec.Body)
		}
	}
	if vols, _ := w.srv.idx.ListVolumesByOwner(w.bob.ID); len(vols) != 1 || vols[0].Name != "bobvol" {
		t.Fatalf("a refused request changed bob's volumes: %+v", vols)
	}
	// Other people are unaffected, and an invalid request doesn't spend anything.
	if _, code := w.testInput(w.annC, "h"); code != http.StatusOK {
		t.Errorf("ann's attempt after bob exhausted his = %d", code)
	}
	if rec := w.req(w.annC, "POST", "/volumes/test", map[string]string{"host": "h"}); rec.Code != http.StatusBadRequest {
		t.Errorf("an incomplete request = %d, want 400", rec.Code)
	}
}

// blockingNAS holds Connect until released, like a host that never answers.
type blockingNAS struct {
	*fakeSMB
	gate chan struct{}
}

func (b *blockingNAS) Connect() error { <-b.gate; return b.connectErr }

func TestOnlyAFewConnectionTestsRunAtOnce(t *testing.T) {
	w := newWorld(t)
	gate := make(chan struct{})
	started := make(chan struct{}, 32)
	w.srv.newSMB = func(host, share, user, pass string) smbConnector {
		started <- struct{}{}
		return &blockingNAS{fakeSMB: newFakeSMB(), gate: gate}
	}

	// Several users, so the per-user budget isn't what stops them.
	var cookies []*http.Cookie
	for i := 0; i <= maxConcurrentTests; i++ {
		_, c := signedInUser(t, w.srv, "u"+w.itoa(int64(i)), index.RoleUser)
		cookies = append(cookies, c)
	}
	var wg sync.WaitGroup
	for _, c := range cookies[:maxConcurrentTests] {
		wg.Add(1)
		go func(c *http.Cookie) {
			defer wg.Done()
			w.testInput(c, "slow.lan")
		}(c)
	}
	for i := 0; i < maxConcurrentTests; i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("the allowed tests didn't start")
		}
	}
	rec := w.req(cookies[maxConcurrentTests], "POST", "/volumes/test", map[string]string{"host": "slow.lan", "share": "s", "username": "u", "password": "p"})
	if rec.Code != http.StatusTooManyRequests || !strings.Contains(rec.Body.String(), "running") {
		t.Fatalf("a test beyond the limit = %d %s, want 429", rec.Code, rec.Body)
	}
	close(gate)
	wg.Wait()
	if _, code := w.testInput(cookies[maxConcurrentTests], "slow.lan"); code != http.StatusOK {
		t.Fatalf("after the others finished = %d, want the slots freed", code)
	}
}
