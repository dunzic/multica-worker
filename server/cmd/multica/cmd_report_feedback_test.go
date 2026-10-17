package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReportFeedbackCLI(t *testing.T) {
	const att = "00000000-0000-4000-8000-000000000001"
	const row = "00000000-0000-4000-8000-000000000002"
	for _, tt := range []struct {
		op, method, suffix string
		write              bool
	}{
		{"get", "GET", "?after=next", false}, {"history", "GET", "/history?before=next", false}, {"configure", "PUT", "/schema", true}, {"save", "PUT", "/rows/" + row, true}, {"archive", "POST", "/rows/" + row + "/archive", true},
	} {
		t.Run(tt.op, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != tt.method || r.URL.RequestURI() != "/api/attachments/"+att+"/report-feedback"+tt.suffix {
					t.Errorf("unexpected route %s %s", r.Method, r.URL)
				}
				if r.Header.Get("X-Workspace-ID") != "ws-1" || r.Header.Get("Authorization") == "" {
					t.Error("missing workspace/auth")
				}
				if tt.write {
					var body map[string]any
					if json.NewDecoder(r.Body).Decode(&body) != nil || body["expected_revision"] != float64(2) {
						t.Error("lost payload revision")
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"revision":3}`))
			}))
			defer srv.Close()
			setCLITestServerEnv(t, srv.URL)
			t.Setenv("MULTICA_TOKEN", "mat_test-token")
			cmd := newReportFeedbackCommand()
			args := []string{tt.op, att}
			if tt.op == "save" || tt.op == "archive" {
				args = append(args, row)
			}
			if tt.write {
				args = append(args, "--file", "-")
			} else {
				args = append(args, "--cursor", "next")
			}
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetIn(strings.NewReader(`{"expected_revision":2}`))
			cmd.SetArgs(args)
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || !strings.Contains(output.String(), `"revision": 3`) {
				t.Fatalf("calls=%d output=%s", calls, output.String())
			}
		})
	}
}

func TestReportFeedbackCLIRejectsInvalidPayload(t *testing.T) {
	for _, body := range []string{`null`, `[]`, `1`, `{} {}`, strings.Repeat(" ", 512*1024) + `{}`} {
		if _, err := readReportPayload(strings.NewReader(body)); err == nil {
			t.Fatal("accepted invalid payload")
		}
	}
}
