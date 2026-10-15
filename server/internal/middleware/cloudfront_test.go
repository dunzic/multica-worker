package middleware

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/auth"
)

// testSigner sets up env vars and creates a CloudFrontSigner with a throwaway RSA key.
func testSigner(t *testing.T) *auth.CloudFrontSigner {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8Bytes, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pemBlock := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8Bytes})
	b64Key := base64.StdEncoding.EncodeToString(pemBlock)

	t.Setenv("CLOUDFRONT_KEY_PAIR_ID", "TESTKEY")
	t.Setenv("CLOUDFRONT_DOMAIN", "cdn.example.com")
	t.Setenv("COOKIE_DOMAIN", ".example.com")
	t.Setenv("CLOUDFRONT_PRIVATE_KEY_SECRET", "")
	t.Setenv("CLOUDFRONT_PRIVATE_KEY", b64Key)

	signer := auth.NewCloudFrontSignerFromEnv()
	if signer == nil {
		t.Fatal("failed to create test CloudFrontSigner")
	}
	return signer
}

func TestExpireCloudFrontCookies(t *testing.T) {
	signer := testSigner(t)
	for _, name := range []string{"", "CloudFront-Policy", "CloudFront-Signature", "CloudFront-Key-Pair-Id"} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if name != "" {
				req.AddCookie(&http.Cookie{Name: name, Value: "old-broad-policy"})
			}
			rec := httptest.NewRecorder()
			ExpireCloudFrontCookies(signer)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			})).ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatal("cleanup blocked the request")
			}
			if name == "" {
				if len(rec.Result().Cookies()) != 0 {
					t.Fatal("missing CDN cookies must not be issued")
				}
			} else {
				wantExpiredCloudFrontCookies(t, rec)
			}
		})
	}
}

func TestExpireCloudFrontCookies_NilSigner(t *testing.T) {
	rec := httptest.NewRecorder()
	ExpireCloudFrontCookies(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK || len(rec.Result().Cookies()) != 0 {
		t.Fatal("nil signer changed the response")
	}
}

// Both the normal API group and Auth-only routes can slide a session. Neither
// may mint another deployment-wide CDN credential or duplicate cleanup headers.
func TestAuth_ExpiresCloudFrontCookiesOnRenewal(t *testing.T) {
	for _, withMiddleware := range []bool{false, true} {
		signer := testSigner(t)
		var handler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		if withMiddleware {
			handler = ExpireCloudFrontCookies(signer)(handler)
		}
		handler = Auth(nil, nil, nil, signer)(handler)
		req := cookieRequest(http.MethodGet, sessionToken(t, auth.AuthRenewThreshold()/2, "sid-cdn"))
		req.AddCookie(&http.Cookie{Name: "CloudFront-Policy", Value: "old-broad-policy"})
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || renewedAuthCookie(rec) == "" {
			t.Fatal("CDN retirement must not break session renewal")
		}
		wantExpiredCloudFrontCookies(t, rec)
	}
}

func wantExpiredCloudFrontCookies(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	counts := map[string]int{
		"CloudFront-Policy": 0, "CloudFront-Signature": 0, "CloudFront-Key-Pair-Id": 0,
	}
	for _, cookie := range rec.Result().Cookies() {
		if _, ok := counts[cookie.Name]; !ok {
			continue
		}
		counts[cookie.Name]++
		if cookie.Value != "" || cookie.MaxAge >= 0 || cookie.Expires.After(time.Now()) ||
			cookie.Domain != "example.com" || cookie.Path != "/" || !cookie.Secure || !cookie.HttpOnly {
			t.Fatalf("legacy cookie not expired with matching scope: %+v", cookie)
		}
	}
	for name, count := range counts {
		if count != 1 {
			t.Fatalf("%s cleanup count=%d, want one", name, count)
		}
	}
}

func TestCloudFrontCookies_NoNewBroadPolicy(t *testing.T) {
	signer := testSigner(t)
	for _, existing := range []bool{false, true} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		if existing {
			req.AddCookie(&http.Cookie{Name: "CloudFront-Policy", Value: "old-broad-policy"})
		}
		rec := httptest.NewRecorder()
		ExpireCloudFrontCookies(signer)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})).ServeHTTP(rec, req)
		for _, cookie := range rec.Result().Cookies() {
			if cookie.Value != "" || cookie.MaxAge >= 0 || cookie.Expires.After(time.Now()) {
				t.Fatalf("new broad CDN credential bypasses API revocation: %s", cookie.Name)
			}
		}
		if existing && len(rec.Result().Cookies()) != 3 {
			t.Fatal("existing broad credentials were not cleared")
		}
	}
}
