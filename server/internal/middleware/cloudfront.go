package middleware

import (
	"net/http"

	"github.com/multica-ai/multica/server/internal/auth"
)

// ExpireCloudFrontCookies retires the old deployment-wide bearer cookies.
// It never replaces a missing cookie with another storage bypass credential.
func ExpireCloudFrontCookies(signer *auth.CloudFrontSigner) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if signer == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !SessionRenewed(r) {
				expiredCookies := signer.ExpiredCookies()
				for _, cookie := range expiredCookies {
					if _, err := r.Cookie(cookie.Name); err == nil {
						// Clear all three even if the browser only sent one.
						for _, expired := range expiredCookies {
							http.SetCookie(w, expired)
						}
						break
					}
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}
