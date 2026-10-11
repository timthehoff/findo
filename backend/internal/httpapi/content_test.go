package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

func TestSetContentHeaders(t *testing.T) {
	tests := []struct {
		name          string
		file          string
		forceDownload bool
		wantType      string
		wantDisp      string
	}{
		{"pdf inline", "Report.PDF", false, "application/pdf", "inline"},
		{"image inline", "photo.jpeg", false, "image/jpeg", "inline"},
		{"text inline", "notes.txt", false, "text/plain; charset=utf-8", "inline"},
		{"video inline", "clip.mp4", false, "video/mp4", "inline"},
		{"force download", "Report.pdf", true, "application/pdf", "attachment"},
		{"office downloads", "budget.xlsx", false, "", "attachment"},
		{"iwork downloads", "budget.numbers", false, "", "attachment"},
		{"html never inline", "page.html", false, "", "attachment"},
		{"svg never inline", "logo.svg", false, "", "attachment"},
		{"no extension", "README", false, "application/octet-stream", "attachment"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := http.Header{}
			setContentHeaders(h, tt.file, tt.forceDownload)

			if tt.wantType != "" && h.Get("Content-Type") != tt.wantType {
				t.Errorf("Content-Type = %q, want %q", h.Get("Content-Type"), tt.wantType)
			}
			if h.Get("Content-Type") == "" {
				t.Error("Content-Type empty")
			}
			if disp := h.Get("Content-Disposition"); !strings.HasPrefix(disp, tt.wantDisp+";") {
				t.Errorf("Content-Disposition = %q, want %s", disp, tt.wantDisp)
			}
			if h.Get("X-Content-Type-Options") != "nosniff" {
				t.Error("missing nosniff")
			}
			if h.Get("Content-Security-Policy") != "sandbox" {
				t.Error("missing sandbox CSP")
			}
		})
	}
}

func TestSetContentHeadersEncodesFilename(t *testing.T) {
	h := http.Header{}
	setContentHeaders(h, "Café \"Q3\".pdf", false)
	disp := h.Get("Content-Disposition")
	if strings.Contains(disp, "\"Q3\"\"") || !strings.Contains(disp, "filename") {
		t.Errorf("filename not safely encoded: %q", disp)
	}
}
