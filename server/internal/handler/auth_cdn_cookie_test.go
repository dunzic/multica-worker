package handler

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/testutil"
)

func TestLoginRetiresBroadCDNCookies(t *testing.T) {
	for _, method := range []string{"email_code", "google"} {
		t.Run(method, func(t *testing.T) {
			email := "cdn-retirement-" + method + "@example.com"
			userID := dbfx.User(t, "CDN retirement user", email)
			h := *testHandler
			h.CFSigner = testCloudFrontSigner(t)

			var loginHandler http.HandlerFunc
			var request *http.Request
			if method == "email_code" {
				dbfx.Insert(t, "verification_code", testutil.Cols{
					"email":      email,
					"code":       "123456",
					"expires_at": testutil.Raw("now() + interval '10 minutes'"),
				})
				loginHandler = h.VerifyCode
				request = testutil.JSONRequest(http.MethodPost, "/auth/verify-code", map[string]string{
					"email": email, "code": "123456",
				})
			} else {
				t.Setenv("GOOGLE_CLIENT_ID", "test-client")
				t.Setenv("GOOGLE_CLIENT_SECRET", "test-secret")
				// Keep OAuth entirely local; no external account or network is used.
				h.googleOAuthHTTPClient = &http.Client{Transport: googleRoundTripper(func(req *http.Request) (*http.Response, error) {
					switch req.URL.Host {
					case "oauth2.googleapis.com":
						return googleResponse(req, http.StatusOK, `{"access_token":"test-token"}`), nil
					case "www.googleapis.com":
						return googleResponse(req, http.StatusOK, fmt.Sprintf(`{"email":%q}`, email)), nil
					default:
						t.Fatalf("unexpected OAuth request: %s", req.URL)
						return nil, nil
					}
				})}
				loginHandler = h.GoogleLogin
				request = testutil.JSONRequest(http.MethodPost, "/auth/google", map[string]string{
					"code": "test-code", "redirect_uri": "http://localhost/auth/callback",
				})
			}

			var login LoginResponse
			resp := testutil.Call(t, loginHandler, request).Want(http.StatusOK).JSON(&login)
			if login.Token == "" || login.User.ID != userID {
				t.Fatal("CDN retirement changed the successful login contract")
			}
			var session, csrf *http.Cookie
			retired := map[string]int{}
			for _, cookie := range resp.Result().Cookies() {
				switch cookie.Name {
				case auth.AuthCookieName:
					session = cookie
				case auth.CSRFCookieName:
					csrf = cookie
				case "CloudFront-Policy", "CloudFront-Signature", "CloudFront-Key-Pair-Id":
					retired[cookie.Name]++
					if cookie.Value != "" || cookie.MaxAge >= 0 || !cookie.Expires.Before(time.Now()) ||
						cookie.Domain != "example.test" || cookie.Path != "/" || !cookie.Secure || !cookie.HttpOnly {
						t.Fatalf("login did not retire the original CDN cookie scope: %s", cookie.Name)
					}
				}
			}
			if len(retired) != 3 {
				t.Fatalf("retired %d CDN cookie names, want 3", len(retired))
			}
			for name, count := range retired {
				if count != 1 {
					t.Fatalf("retired %s %d times, want once", name, count)
				}
			}
			if session == nil || session.Value != login.Token || csrf == nil || csrf.Value == "" {
				t.Fatal("login must preserve the browser session and CSRF cookies")
			}
			meRequest := newRequest(http.MethodGet, "/users/me", nil)
			meRequest.AddCookie(session)
			protected := middleware.Auth(h.Queries, nil, nil, nil)(http.HandlerFunc(h.GetMe))
			var me UserResponse
			testutil.Call(t, protected.ServeHTTP, meRequest).Want(http.StatusOK).JSON(&me)
			if me.ID != userID {
				t.Fatal("login session no longer authenticates the expected user")
			}
		})
	}
}
