package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRunUpdateRejectsNonPositiveDownloadTimeout(t *testing.T) {
	orig := updateDownloadTimeout
	updateDownloadTimeout = 0
	t.Cleanup(func() { updateDownloadTimeout = orig })

	err := runUpdate(nil, nil)
	if err == nil || !strings.Contains(err.Error(), "download timeout must be greater than zero") {
		t.Fatalf("runUpdate error = %v, want download timeout validation", err)
	}
}

func TestRunUpdateGuards(t *testing.T) {
	oldVersion := version
	t.Cleanup(func() { version = oldVersion })
	for _, tc := range []struct{ name, current, latest, wantError string }{
		{"development build", "0.6.2-135-g73be2734f-dirty", "9.0.0", "development"},
		{"unknown build", "dev", "9.0.0", "development"},
		{"below baseline", "0.6.2", "0.1.0", "below recorded minimum"},
		{"no downgrade", "9.1.0", "9.0.0", ""},
		{"equal version", "9.0.0", "9.0.0", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Path != "/repos/dunzic/multica-worker/releases/latest" {
					t.Errorf("unexpected download: %s", r.URL.Path)
				}
				_, _ = fmt.Fprintf(w, `{"tag_name":%q}`, "v"+tc.latest)
			}))
			defer srv.Close()
			t.Setenv("MULTICA_RELEASE_API_BASE_URL", srv.URL)
			version = tc.current
			err := runUpdate(nil, nil)
			if tc.wantError == "" && err != nil {
				t.Fatal(err)
			}
			if tc.wantError != "" && (err == nil || !strings.Contains(err.Error(), tc.wantError)) {
				t.Fatalf("got %v, want %s", err, tc.wantError)
			}
			if tc.wantError == "development" && requests.Load() != 0 {
				t.Fatal("development build must not contact releases")
			}
		})
	}
}
