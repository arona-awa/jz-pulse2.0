package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"clearlove/internal/config"
)

func TestStaticAssetCacheAndNoCSRFCookie(t *testing.T) {
	oldInstalled := config.Cfg.Installed
	config.Cfg.Installed = true
	defer func() { config.Cfg.Installed = oldInstalled }()

	h := Use(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	cases := []struct {
		path    string
		wantCC  string
		wantSet bool
	}{
		{"/static/app.js?v=2.1.58", "public, max-age=31536000, immutable", false},
		{"/static/dina-jz-logo.jpg", "public, max-age=86400", false},
		{"/uploads/20260101/a.webp", "public, max-age=31536000, immutable", false},
		{"/theme.css?v=2.1.58", "public, max-age=300", false},
		{"/favicon.ico", "public, max-age=604800", false},
	}
	for _, c := range cases {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, c.path, nil)
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: status = %d", c.path, rr.Code)
		}
		if got := rr.Header().Get("Cache-Control"); got != c.wantCC {
			t.Fatalf("%s: Cache-Control = %q, want %q", c.path, got, c.wantCC)
		}
		if got := rr.Header().Get("Set-Cookie"); got != "" {
			t.Fatalf("%s: unexpected Set-Cookie %q", c.path, got)
		}
	}
}

func TestHTMLStillGetsCSRFCookie(t *testing.T) {
	oldInstalled := config.Cfg.Installed
	config.Cfg.Installed = true
	defer func() { config.Cfg.Installed = oldInstalled }()

	h := Use(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.TrimSpace(CSRFToken(r)) == "" {
			t.Fatal("CSRF token missing from request context")
		}
		w.WriteHeader(http.StatusOK)
	}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if got := rr.Header().Get("Set-Cookie"); !strings.Contains(got, "clv_csrf=") {
		t.Fatalf("HTML response should set clv_csrf, got %q", got)
	}
}
