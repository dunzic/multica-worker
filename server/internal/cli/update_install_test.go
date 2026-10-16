package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/buildinfo"
)

func TestUpdateDownloadCompatibilityAtomic(t *testing.T) {
	// Only the disposable copy of this test executable may replace itself.
	if os.Getenv("MULTICA_TEST_UPDATE_CHILD") == "1" {
		if _, err := UpdateViaDownloadWithTimeout("v9.0.0", 5*time.Second); err != nil {
			t.Fatal(err)
		}
		return
	}
	if runtime.GOOS == "windows" {
		t.Skip("POSIX synthetic executable; Windows native acceptance is separate")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"valid", "wrong version", "missing capabilities", "corrupt archive"} {
		t.Run(scenario, func(t *testing.T) {
			manifest, err := buildinfo.Current()
			if err != nil {
				t.Fatal(err)
			}
			report := buildinfo.CLIReport{Version: "9.0.0", BuildCommit: strings.Repeat("a", 40), Distribution: manifest, Capabilities: manifest.RequiredCLICapabilities}
			if scenario == "wrong version" {
				report.Version = "9.0.1"
			}
			if scenario == "missing capabilities" {
				report.Capabilities = nil
			}
			metadata, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			payload := []byte("#!/bin/sh\nprintf '%s\\n' '" + string(metadata) + "'\n")
			var archive bytes.Buffer
			gz := gzip.NewWriter(&archive)
			tw := tar.NewWriter(gz)
			if err := tw.WriteHeader(&tar.Header{Name: "multica", Mode: 0755, Size: int64(len(payload))}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write(payload); err != nil {
				t.Fatal(err)
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			hash := fmt.Sprintf("%x", sha256.Sum256(archive.Bytes()))
			asset := fmt.Sprintf("multica-cli-9.0.0-%s-%s.tar.gz", runtime.GOOS, runtime.GOARCH)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				endpoint := "http://" + r.Host
				switch r.URL.Path {
				case "/repos/dunzic/multica-worker/releases/tags/v9.0.0":
					_ = json.NewEncoder(w).Encode(GitHubRelease{TagName: "v9.0.0", Assets: []GitHubReleaseAsset{
						{Name: asset, BrowserDownloadURL: endpoint + "/archive"},
						{Name: ChecksumManifestName, BrowserDownloadURL: endpoint + "/checksums"},
					}})
				case "/checksums":
					_, _ = fmt.Fprintf(w, "%s  %s\n", hash, asset)
				case "/archive":
					if scenario == "corrupt archive" {
						_, _ = w.Write([]byte("corrupt"))
					} else {
						_, _ = w.Write(archive.Bytes())
					}
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()
			dir := t.TempDir()
			bin := filepath.Join(dir, "multica-test")
			if err := os.WriteFile(bin, original, 0755); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(bin, "-test.run=^TestUpdateDownloadCompatibilityAtomic$")
			cmd.Env = append(os.Environ(), "MULTICA_TEST_UPDATE_CHILD=1", releaseAPIBaseURLEnv+"="+srv.URL, releaseDownloadBaseURLEnv+"=")
			output, runErr := cmd.CombinedOutput()
			actual, err := os.ReadFile(bin)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "valid" {
				if runErr != nil {
					t.Fatalf("update: %v\n%s", runErr, output)
				}
				if !bytes.Equal(actual, payload) {
					t.Fatal("compatible payload was not installed")
				}
			} else {
				if runErr == nil {
					t.Fatalf("accepted %s", scenario)
				}
				if !bytes.Equal(actual, original) {
					t.Fatal("failed update changed original executable")
				}
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 {
				t.Fatalf("staging files leaked: %v %v", entries, err)
			}
		})
	}
}

func TestUpdateDownloadRejectsBelowBaselineBeforeNetwork(t *testing.T) {
	t.Setenv(releaseAPIBaseURLEnv, "http://127.0.0.1:1")
	if _, err := UpdateViaDownload("0.1.0"); err == nil || !strings.Contains(err.Error(), "below recorded minimum") {
		t.Fatalf("expected local baseline rejection, got %v", err)
	}
}
