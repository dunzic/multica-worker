package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGoogleLoginRejectedForPrivateDeployment(t *testing.T) {
	t.Setenv("MULTICA_PRIVATE_DEPLOYMENT", "true")
	t.Setenv("GOOGLE_CLIENT_ID", "configured-but-disabled")
	t.Setenv("GOOGLE_CLIENT_SECRET", "configured-but-disabled")

	req := httptest.NewRequest(
		http.MethodPost,
		"/auth/google",
		strings.NewReader(`{"code":"must-not-be-exchanged"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	testHandler.GoogleLogin(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("GoogleLogin: expected 403, got %d: %s", w.Code, w.Body.String())
	}
}
