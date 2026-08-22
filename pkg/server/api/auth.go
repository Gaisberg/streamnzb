package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"streamnzb/pkg/auth"
	"streamnzb/pkg/core/httpx"
	"streamnzb/pkg/core/logger"
)

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type LoginResponse struct {
	Success            bool   `json:"success"`
	Token              string `json:"token,omitempty"`
	User               string `json:"user,omitempty"`
	MustChangePassword bool   `json:"must_change_password,omitempty"`
	Error              string `json:"error,omitempty"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}

	addr := httpx.ClientIP(r)
	if wait := s.loginThrottle.retryAfter(addr, time.Now()); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait/time.Second)))
		logger.Warn("Login attempt throttled", "remote", addr, "retry_after", wait)
		writeJSON(w, http.StatusTooManyRequests, LoginResponse{
			Success: false,
			Error:   "Too many failed login attempts. Try again later.",
		})
		return
	}

	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	// The username is deliberately not checked here first. Authenticate
	// compares it in constant time and verifies the password either way, so a
	// wrong username cannot be told from a wrong password by how fast the
	// rejection came back.
	adminUsername := s.adminUsername()
	stream, err := s.streamManager.Authenticate(req.Username, req.Password, adminUsername, s.adminPasswordHash(), s.adminToken())
	if err != nil {
		s.loginThrottle.recordFailure(addr, time.Now())
		writeJSON(w, http.StatusUnauthorized, LoginResponse{
			Success: false,
			Error:   "Invalid credentials",
		})
		return
	}
	s.loginThrottle.recordSuccess(addr)

	// The plaintext is only in hand here, so this is the one moment a hash
	// stored in an older format can be replaced without asking the admin to
	// retype anything.
	s.upgradeAdminPasswordHash(req.Password)

	http.SetCookie(w, auth.SessionCookie(r, stream.Token, auth.SessionCookieMaxAge))

	var mustChangePassword bool
	if stream.Username == s.adminUsername() {
		mustChangePassword = s.adminMustChangePassword()
	}

	writeJSON(w, http.StatusOK, LoginResponse{
		Success:            true,
		Token:              stream.Token,
		User:               stream.Username,
		MustChangePassword: mustChangePassword,
	})
}

// upgradeAdminPasswordHash rewrites the stored admin password with the current
// hashing parameters when the stored one predates them — an unsalted SHA-256
// digest from before argon2id, or an argon2id hash written with weaker
// settings. It runs after a successful login because that is the only point
// where the plaintext is available; a failure to persist is logged rather than
// surfaced, since the login itself succeeded and the old hash still works.
func (s *Server) upgradeAdminPasswordHash(password string) {
	stored := s.adminPasswordHash()
	if !auth.PasswordNeedsRehash(stored) {
		return
	}

	newHash, err := auth.HashPassword(password)
	if err != nil {
		logger.Warn("Failed to rehash admin password", "err", err)
		return
	}

	s.mu.Lock()
	// A change-password request may have landed while the hash was being
	// computed. Leave the newer value alone rather than overwriting it with a
	// hash of the older password.
	if s.config.AdminPasswordHash != stored {
		s.mu.Unlock()
		return
	}
	s.config.AdminPasswordHash = newHash
	// Save the config this write landed on, not whatever s.config points at by
	// the time the disk write starts — a reload may have swapped it since.
	cfg := s.config
	s.mu.Unlock()

	if err := cfg.Save(); err != nil {
		logger.Warn("Failed to persist upgraded admin password hash", "err", err)
		return
	}
	logger.Info("Admin password hash upgraded to the current format")
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodGet) {
		return
	}
	version := "dev"
	if s.strmServer != nil {
		version = s.strmServer.Version()
	}
	writeJSON(w, http.StatusOK, map[string]string{"version": version})
}

func (s *Server) handleAuthCheck(w http.ResponseWriter, r *http.Request) {
	stream, ok := auth.StreamFromContext(r)
	cookiePresent := false
	bearerPresent := false
	authViaCookie := false
	if !ok {

		cookie, err := r.Cookie(auth.SessionCookieName)
		if err == nil && cookie != nil {
			cookiePresent = true
			stream, err = s.streamManager.AuthenticateToken(cookie.Value, s.adminUsername(), s.adminToken())
			if err == nil {
				logger.Debug("Auth check authenticated", "via", "cookie")
				authViaCookie = true
				ok = true
			}
		}
	}

	if !ok {
		authHeader := r.Header.Get("Authorization")
		if authHeader != "" {
			bearerPresent = true
			parts := strings.SplitN(authHeader, " ", 2)
			if len(parts) == 2 && parts[0] == "Bearer" {
				var err error
				stream, err = s.streamManager.AuthenticateToken(parts[1], s.adminUsername(), s.adminToken())
				if err == nil {
					logger.Debug("Auth check authenticated", "via", "bearer")
					ok = true
				}
			}
		}
	}

	logger.Debug("Auth check evaluated", "ok", ok, "cookie_present", cookiePresent, "bearer_present", bearerPresent)

	if ok {
		var mustChangePassword bool
		if stream.Username == s.adminUsername() {
			mustChangePassword = s.adminMustChangePassword()
		}
		out := map[string]interface{}{
			"authenticated":        true,
			"username":             stream.Username,
			"must_change_password": mustChangePassword,
		}
		if !authViaCookie {
			out["token"] = stream.Token
		}
		if s.strmServer != nil {
			out["version"] = s.strmServer.Version()
		}
		writeJSON(w, http.StatusOK, out)
	} else {
		writeJSON(w, http.StatusUnauthorized, map[string]interface{}{
			"authenticated": false,
		})
	}
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if !requireMethod(w, r, http.MethodPost) {
		return
	}
	http.SetCookie(w, auth.ClearSessionCookie(r))
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r, "Forbidden", http.MethodPost) {
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request"})
		return
	}
	adminUsername := s.adminUsername()
	if req.Username != "" && req.Username != adminUsername {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid username"})
		return
	}
	if len(strings.TrimSpace(req.Password)) < 6 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Password must be at least 6 characters long"})
		return
	}
	newHash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to hash password"})
		return
	}
	s.mu.Lock()
	s.config.AdminPasswordHash = newHash
	s.config.AdminMustChangePassword = false
	cfg := s.config
	s.mu.Unlock()
	if err := cfg.Save(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "Password updated successfully",
	})
}
