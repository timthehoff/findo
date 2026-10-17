package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/thoff/findo/backend/internal/index"
)

func decodeContent(t *testing.T, body []byte) index.ContentSettings {
	t.Helper()
	var out struct {
		Content index.ContentSettings `json:"content"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return out.Content
}

func TestSettingsDefaultsAndPartialUpdate(t *testing.T) {
	srv, _, _ := newTestServer(t)

	rec := doJSON(t, srv, "GET", "/settings", nil)
	if rec.Code != http.StatusOK || decodeContent(t, rec.Body.Bytes()) != index.DefaultContentSettings() {
		t.Fatalf("GET /settings = %d %s, want the defaults", rec.Code, rec.Body)
	}

	// The pause button sends only {"paused": true}; everything else must stay.
	rec = doJSON(t, srv, "PUT", "/settings", map[string]interface{}{"content": map[string]interface{}{"paused": true}})
	want := index.DefaultContentSettings()
	want.Paused = true
	if rec.Code != http.StatusOK || decodeContent(t, rec.Body.Bytes()) != want {
		t.Fatalf("partial PUT = %d %s, want %+v", rec.Code, rec.Body, want)
	}

	rec = doJSON(t, srv, "PUT", "/settings", map[string]interface{}{"content": map[string]interface{}{"workers": 3, "delayMs": 200}})
	want.Workers, want.DelayMS = 3, 200
	if rec.Code != http.StatusOK || decodeContent(t, rec.Body.Bytes()) != want {
		t.Fatalf("second PUT = %d %s, want %+v", rec.Code, rec.Body, want)
	}
	if got := decodeContent(t, doJSON(t, srv, "GET", "/settings", nil).Body.Bytes()); got != want {
		t.Fatalf("GET after PUTs = %+v, want %+v (and still paused)", got, want)
	}
}

func TestSettingsRejectsBadValuesWithoutChangingAnything(t *testing.T) {
	srv, _, _ := newTestServer(t)
	for name, body := range map[string]interface{}{
		"zero workers":  map[string]interface{}{"content": map[string]interface{}{"workers": 0}},
		"tiny file cap": map[string]interface{}{"content": map[string]interface{}{"maxFileBytes": 10}},
	} {
		rec := doJSON(t, srv, "PUT", "/settings", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d %s, want 400", name, rec.Code, rec.Body)
		}
	}
	if got := decodeContent(t, doJSON(t, srv, "GET", "/settings", nil).Body.Bytes()); got != index.DefaultContentSettings() {
		t.Fatalf("settings changed after rejected PUTs: %+v", got)
	}

	rec := doJSON(t, srv, "PUT", "/settings", "not an object")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("malformed body: status = %d, want 400", rec.Code)
	}
}

func TestVolumeContentIndexThroughTheAPI(t *testing.T) {
	srv := newTestServerNoVolumes(t, newFakeSMB())

	create := doJSON(t, srv, "POST", "/volumes", map[string]interface{}{
		"name": "nas", "host": "h", "share": "s", "username": "u", "password": "p", "contentIndex": true,
	})
	var vol index.Volume
	if err := json.Unmarshal(create.Body.Bytes(), &vol); err != nil || create.Code != http.StatusCreated || !vol.ContentIndex {
		t.Fatalf("create with contentIndex = %d %s", create.Code, create.Body)
	}

	// Omitting the field on update leaves the opt-in alone...
	upd := doJSON(t, srv, "PUT", "/volumes/"+strconv.FormatInt(vol.ID, 10), map[string]interface{}{
		"name": "nas", "host": "h2", "share": "s", "username": "u",
	})
	if err := json.Unmarshal(upd.Body.Bytes(), &vol); err != nil || !vol.ContentIndex {
		t.Fatalf("update without the field dropped the opt-in: %s", upd.Body)
	}
	// ...and an explicit false opts out.
	upd = doJSON(t, srv, "PUT", "/volumes/"+strconv.FormatInt(vol.ID, 10), map[string]interface{}{
		"name": "nas", "host": "h2", "share": "s", "username": "u", "contentIndex": false,
	})
	vol = index.Volume{}
	if err := json.Unmarshal(upd.Body.Bytes(), &vol); err != nil || vol.ContentIndex {
		t.Fatalf("explicit contentIndex:false did not opt out: %s", upd.Body)
	}
}
