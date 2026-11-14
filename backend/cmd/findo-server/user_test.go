package main

import (
	"bytes"
	"database/sql"
	"net/url"
	"strings"
	"testing"

	"github.com/thoff/findo/backend/internal/auth"
	"github.com/thoff/findo/backend/internal/crypto"
	"github.com/thoff/findo/backend/internal/index"
	"github.com/thoff/findo/backend/internal/testdb"
)

var cliKey = bytes.Repeat([]byte{7}, crypto.KeySize)

type cliEnv struct {
	t   *testing.T
	idx *index.Index
	raw *sql.DB
	pub *url.URL
}

func newCLIEnv(t *testing.T) *cliEnv {
	t.Helper()
	dsn := testdb.DSN(t)
	idx, err := index.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.Close(); idx.Close() })
	pub, _ := url.Parse("https://findo.example.com")
	return &cliEnv{t: t, idx: idx, raw: raw, pub: pub}
}

func (e *cliEnv) run(args ...string) (string, error) {
	var out bytes.Buffer
	err := userCommand(args, e.idx, cliKey, e.pub, &out)
	return out.String(), err
}

// legacyVolume inserts a volume as it existed before accounts: no owner, its
// password sealed with no authenticated data.
func (e *cliEnv) legacyVolume(name, password string, key []byte) int64 {
	e.t.Helper()
	enc, err := crypto.Encrypt(key, []byte(password), nil)
	if err != nil {
		e.t.Fatal(err)
	}
	var id int64
	if err := e.raw.QueryRow(`INSERT INTO volumes (name, host, share, username, password_enc, created_at, updated_at)
		VALUES ($1, 'h', 's', 'u', $2, now(), now()) RETURNING id`, name, enc).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func linkToken(t *testing.T, out string) string {
	t.Helper()
	for _, f := range strings.Fields(out) {
		if strings.HasPrefix(f, "https://findo.example.com/enroll#") {
			return strings.TrimPrefix(f, "https://findo.example.com/enroll#")
		}
	}
	t.Fatalf("no enrollment link in:\n%s", out)
	return ""
}

func TestTheFirstAccountMustBeAnAdmin(t *testing.T) {
	e := newCLIEnv(t)
	if _, err := e.run("add", "ann"); err == nil || !strings.Contains(err.Error(), "--admin") {
		t.Fatalf("a first non-admin = %v, want an error pointing at --admin", err)
	}
	if n, _ := e.idx.CountUsers(); n != 0 {
		t.Fatalf("a refused add created %d users", n)
	}
	out, err := e.run("add", "--admin", "ann")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `Created admin "ann"`) {
		t.Errorf("output: %s", out)
	}
	// Once there is an admin, ordinary users need no flag.
	if _, err := e.run("add", "bob"); err != nil {
		t.Fatalf("a second, ordinary user: %v", err)
	}
	if u, _, _ := e.idx.GetUserByName("bob"); u.Role != index.RoleUser {
		t.Errorf("bob's role = %q", u.Role)
	}
}

func TestAddPrintsALinkThatEnrolls(t *testing.T) {
	e := newCLIEnv(t)
	out, err := e.run("add", "--admin", "ann")
	if err != nil {
		t.Fatal(err)
	}
	token := linkToken(t, out)
	enr, err := e.idx.LookupEnrollment(auth.HashToken(token))
	if err != nil || enr.User.Username != "ann" {
		t.Fatalf("the printed token doesn't look up: %+v %v", enr, err)
	}
	if strings.Contains(out, "FINDO_PUBLIC_URL isn't set") {
		t.Error("the localhost warning must only show for the default URL")
	}

	local, _ := url.Parse("http://localhost:8080")
	e.pub = local
	var warn bytes.Buffer
	if err := userCommand([]string{"add", "bob"}, e.idx, cliKey, local, &warn); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(warn.String(), "http://localhost:8080/enroll#") || !strings.Contains(warn.String(), "FINDO_PUBLIC_URL isn't set") {
		t.Errorf("localhost output: %s", warn.String())
	}
}

func TestTheFlagMayComeBeforeOrAfterTheName(t *testing.T) {
	e := newCLIEnv(t)
	if _, err := e.run("add", "ann", "--admin"); err != nil {
		t.Fatalf("name then flag: %v", err)
	}
	if u, _, _ := e.idx.GetUserByName("ann"); u.Role != index.RoleAdmin {
		t.Fatalf("role %q", u.Role)
	}
}

func TestTheFirstAdminAdoptsExistingVolumes(t *testing.T) {
	e := newCLIEnv(t)
	one, two := e.legacyVolume("nas", "pw-one", cliKey), e.legacyVolume("media", "pw-two", cliKey)

	out, err := e.run("add", "--admin", "ann")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "owns the 2 volume(s)") {
		t.Errorf("output should say what was adopted: %s", out)
	}
	ann, _, _ := e.idx.GetUserByName("ann")
	for id, want := range map[int64]string{one: "pw-one", two: "pw-two"} {
		secret, err := e.idx.GetVolumeSecret(id)
		if err != nil || secret.OwnerID == nil || *secret.OwnerID != ann.ID {
			t.Fatalf("volume %d owner: %+v %v", id, secret, err)
		}
		got, err := crypto.Decrypt(cliKey, secret.PasswordEnc, index.VolumeAAD(id, secret.OwnerID))
		if err != nil || string(got) != want {
			t.Fatalf("volume %d password after adoption = %q, %v", id, got, err)
		}
		if _, err := crypto.Decrypt(cliKey, secret.PasswordEnc, nil); err == nil {
			t.Errorf("volume %d is still sealed in the legacy form", id)
		}
	}

	// Later users adopt nothing: volumes added since belong to whoever added them.
	e.legacyVolume("late", "pw", cliKey)
	if out, err := e.run("add", "bob"); err != nil || strings.Contains(out, "volume(s)") {
		t.Errorf("a later user: %s %v", out, err)
	}
	if ids, _ := e.idx.VolumeIDsByOwner(ann.ID); len(ids) != 2 {
		t.Errorf("ann's volumes: %v", ids)
	}
}

func TestAFailedAdoptionLeavesNoAccountBehind(t *testing.T) {
	e := newCLIEnv(t)
	e.legacyVolume("nas", "pw", bytes.Repeat([]byte{9}, crypto.KeySize)) // sealed under a different master key

	if _, err := e.run("add", "--admin", "ann"); err == nil || !strings.Contains(err.Error(), "existing volumes") {
		t.Fatalf("add with an undecryptable volume = %v", err)
	}
	if n, _ := e.idx.CountUsers(); n != 0 {
		t.Fatalf("the user was left behind (%d), so retrying would skip adoption", n)
	}
}

func TestEnrollIssuesAReplacementLink(t *testing.T) {
	e := newCLIEnv(t)
	e.run("add", "--admin", "ann")
	ann, _, _ := e.idx.GetUserByName("ann")
	e.idx.AddCredential(index.Credential{UserID: ann.ID, CredentialID: []byte("c"), PublicKey: []byte("k"), Label: "old phone"})

	out, err := e.run("enroll", "ANN")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.idx.LookupEnrollment(auth.HashToken(linkToken(t, out))); err != nil {
		t.Fatalf("recovery link doesn't look up: %v", err)
	}
	if creds, _ := e.idx.ListCredentials(ann.ID); len(creds) != 1 {
		t.Error("issuing a link must not touch existing passkeys")
	}
	// The newest link is the only one that works.
	first := linkToken(t, out)
	out2, _ := e.run("enroll", "ann")
	if _, err := e.idx.LookupEnrollment(auth.HashToken(first)); err == nil {
		t.Error("the earlier link still works")
	}
	if _, err := e.idx.LookupEnrollment(auth.HashToken(linkToken(t, out2))); err != nil {
		t.Errorf("the newest link: %v", err)
	}
}

func TestEnrollRefusesUnknownAndDisabledUsers(t *testing.T) {
	e := newCLIEnv(t)
	e.run("add", "--admin", "ann")
	if _, err := e.run("enroll", "nobody"); err == nil || !strings.Contains(err.Error(), "no user named") {
		t.Errorf("unknown user = %v", err)
	}
	ann, _, _ := e.idx.GetUserByName("ann")
	yes := true
	e.idx.UpdateUser(ann.ID, nil, &yes)
	if _, err := e.run("enroll", "ann"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Errorf("disabled user = %v", err)
	}
}

func TestBadUserCommandsExplainUsage(t *testing.T) {
	e := newCLIEnv(t)
	for name, args := range map[string][]string{
		"no command":       {},
		"unknown command":  {"remove", "ann"},
		"add with no name": {"add"},
		"two names":        {"add", "--admin", "a", "b"},
		"a bad name":       {"add", "--admin", "a b"},
		"an unknown flag":  {"add", "--root", "ann"},
		"enroll no name":   {"enroll"},
	} {
		if _, err := e.run(args...); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if _, err := e.run(); err == nil || !strings.Contains(err.Error(), "findo-server user add") {
		t.Errorf("the usage should show the commands: %v", err)
	}
	e.run("add", "--admin", "ann")
	if _, err := e.run("add", "ANN"); err == nil || !strings.Contains(err.Error(), "already taken") {
		t.Errorf("a duplicate name = %v", err)
	}
}
