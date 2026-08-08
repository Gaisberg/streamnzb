package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"streamnzb/pkg/auth"
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

	var req LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	adminUsername := s.adminUsername()
	if req.Username != adminUsername {
		writeJSON(w, http.StatusUnauthorized, LoginResponse{
			Success: false,
			Error:   "Invalid credentials",
		})
		return
	}

	stream, err := s.streamManager.Authenticate(req.Username, req.Password, adminUsername, s.config.AdminPasswordHash, s.config.AdminToken)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, LoginResponse{
			Success: false,
			Error:   "Invalid credentials",
		})
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "auth_session",
		Value:    stream.Token,
		Path:     "/",
		HttpOnly: true,
		Secure:   false,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   86400 * 7,
	})

	var mustChangePassword bool
	if stream.Username == s.adminUsername() {
		mustChangePassword = s.config.AdminMustChangePassword
	}

	writeJSON(w, http.StatusOK, LoginResponse{
		Success:            true,
		Token:              stream.Token,
		User:               stream.Username,
		MustChangePassword: mustChangePassword,
	})
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

		cookie, err := r.Cookie("auth_session")
		if err == nil && cookie != nil {
			cookiePresent = true
			stream, err = s.streamManager.AuthenticateToken(cookie.Value, s.adminUsername(), s.config.AdminToken)
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
				stream, err = s.streamManager.AuthenticateToken(parts[1], s.adminUsername(), s.config.AdminToken)
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
			mustChangePassword = s.config.AdminMustChangePassword
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
	http.SetCookie(w, &http.Cookie{
		Name:     "auth_session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   false,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
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
	newHash := auth.HashPassword(req.Password)
	s.mu.Lock()
	s.config.AdminPasswordHash = newHash
	s.config.AdminMustChangePassword = false
	s.mu.Unlock()
	if err := s.config.Save(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"success": true,
		"message": "Password updated successfully",
	})
}
