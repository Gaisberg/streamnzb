package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

type contextKey string

const userContextKey contextKey = "user"

func StreamFromContext(r *http.Request) (*Stream, bool) {
	stream, ok := r.Context().Value(userContextKey).(*Stream)
	return stream, ok
}

// ContextWithStream stores the authenticated stream in the context.
func ContextWithStream(ctx context.Context, stream *Stream) context.Context {
	return context.WithValue(ctx, userContextKey, stream)
}

func AuthMiddleware(streamManager *StreamManager, getAdminUsername, getAdminToken func() string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// An outer layer — trusted-proxy auth — may already have settled
			// who this is. Its word stands; the credential checks below are
			// for requests nobody has vouched for yet.
			if _, ok := StreamFromContext(r); ok {
				next.ServeHTTP(w, r)
				return
			}
			adminUsername := ""
			if getAdminUsername != nil {
				adminUsername = getAdminUsername()
			}
			adminToken := ""
			if getAdminToken != nil {
				adminToken = getAdminToken()
			}
			var stream *Stream
			var err error

			cookie, err := r.Cookie(SessionCookieName)
			if err == nil && cookie != nil {
				stream, err = streamManager.AuthenticateToken(cookie.Value, adminUsername, adminToken)
				if err == nil {
					ctx := context.WithValue(r.Context(), userContextKey, stream)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
				// Cookie present but invalid (e.g. after container restart with a new token).
				// Clear it so the browser doesn't keep sending a stale credential.
				http.SetCookie(w, ClearSessionCookie(r))
			}

			authHeader := r.Header.Get("Authorization")
			if authHeader != "" {
				parts := strings.SplitN(authHeader, " ", 2)
				if len(parts) == 2 && parts[0] == "Bearer" {
					token := parts[1]
					stream, err = streamManager.AuthenticateToken(token, adminUsername, adminToken)
					if err == nil {
						ctx := context.WithValue(r.Context(), userContextKey, stream)
						next.ServeHTTP(w, r.WithContext(ctx))
						return
					}
				}
			}

			token := r.URL.Query().Get("token")
			if token != "" {
				stream, err = streamManager.AuthenticateToken(token, adminUsername, adminToken)
				if err == nil {
					ctx := context.WithValue(r.Context(), userContextKey, stream)
					next.ServeHTTP(w, r.WithContext(ctx))
					return
				}
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{
				"error": "Unauthorized",
			})
		})
	}
}

// StreamAuthMiddleware authenticates stream requests using the shared stream manager.
func StreamAuthMiddleware(streamManager *StreamManager, getAdminUsername, getAdminToken func() string) func(http.Handler) http.Handler {
	return AuthMiddleware(streamManager, getAdminUsername, getAdminToken)
}
