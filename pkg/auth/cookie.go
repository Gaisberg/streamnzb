package auth

import (
	"net/http"

	"streamnzb/pkg/core/httpx"
)

const (
	// SessionCookieName is the admin session cookie. Referenced wherever the
	// cookie is read or written so the name exists in exactly one place.
	SessionCookieName = "auth_session"
	// SessionCookieMaxAge is how long a browser keeps the session, in seconds.
	SessionCookieMaxAge = 7 * 24 * 60 * 60
)

// SessionCookie builds the admin session cookie for the request it will be
// sent in response to.
//
// Secure follows the scheme the client actually used rather than being fixed:
// hardcoding it off shipped the session cookie unprotected to every install
// behind a TLS proxy, and hardcoding it on would break plain-HTTP access on a
// LAN, which is how most instances are first set up.
func SessionCookie(r *http.Request, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     SessionCookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   httpx.IsSecure(r),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   maxAge,
	}
}

// ClearSessionCookie builds the cookie that expires an existing session — on
// logout, or when a stale token is presented after the server has restarted
// with a new one.
func ClearSessionCookie(r *http.Request) *http.Cookie {
	return SessionCookie(r, "", -1)
}
