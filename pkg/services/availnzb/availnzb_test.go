package availnzb

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"testing"
	"time"

	"streamnzb/pkg/core/logger"
)

func init() {
	logger.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestGetMeIncludesAPIKeyAndDecodesResponse(t *testing.T) {
	t.Parallel()

	const apiKey = "secret-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %q, want %q", r.Method, http.MethodGet)
		}
		if r.URL.Path != "/api/v1/me" {
			t.Fatalf("path = %q, want %q", r.URL.Path, "/api/v1/me")
		}
		if got := r.Header.Get("X-API-Key"); got != apiKey {
			t.Fatalf("X-API-Key = %q, want %q", got, apiKey)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"app-1","name":"StreamNZB","is_active":true,"app_source":"self_service","trust_level":"trusted","trust_score":97.5,"report_count":7,"public_report_count":2,"verified_report_count":5,"quarantined_report_count":1,"rolled_back_report_count":0,"last_report_at":"2026-03-09T12:34:56Z","last_rollback_at":null}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, apiKey)
	client.HTTP = server.Client()

	got, err := client.GetMe()
	if err != nil {
		t.Fatalf("GetMe: %v", err)
	}
	if got == nil {
		t.Fatal("GetMe returned nil response")
	}
	if got.ID != "app-1" || got.Name != "StreamNZB" || got.TrustLevel != "trusted" {
		t.Fatalf("unexpected response: %+v", got)
	}
	if got.LastReportAt == nil || !got.LastReportAt.Equal(time.Date(2026, 3, 9, 12, 34, 56, 0, time.UTC)) {
		t.Fatalf("LastReportAt = %v, want 2026-03-09T12:34:56Z", got.LastReportAt)
	}
	if got.LastRollbackAt != nil {
		t.Fatalf("LastRollbackAt = %v, want nil", got.LastRollbackAt)
	}
}

func TestGetMeDecodesNumericID(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":123,"name":"StreamNZB","is_active":true,"app_source":"self_service","trust_level":"trusted","trust_score":97.5,"report_count":7,"public_report_count":2,"verified_report_count":5,"quarantined_report_count":1,"rolled_back_report_count":0,"last_report_at":null,"last_rollback_at":null}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "secret-key")
	client.HTTP = server.Client()

	got, err := client.GetMe()
	if err != nil {
		t.Fatalf("GetMe: %v", err)
	}
	if got == nil {
		t.Fatal("GetMe returned nil response")
	}
	if got.ID != "123" {
		t.Fatalf("ID = %q, want %q", got.ID, "123")
	}
}

func TestGetMeDecodesNumericTrustLevel(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"app-1","name":"StreamNZB","is_active":true,"app_source":"self_service","trust_level":4,"trust_score":97.5,"report_count":7,"public_report_count":2,"verified_report_count":5,"quarantined_report_count":1,"rolled_back_report_count":0,"last_report_at":null,"last_rollback_at":null}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "secret-key")
	client.HTTP = server.Client()

	got, err := client.GetMe()
	if err != nil {
		t.Fatalf("GetMe: %v", err)
	}
	if got == nil {
		t.Fatal("GetMe returned nil response")
	}
	if got.TrustLevel != "4" {
		t.Fatalf("TrustLevel = %q, want %q", got.TrustLevel, "4")
	}
}

func TestGetMeDecodesLegacyTimestampFormat(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"app-1","name":"StreamNZB","is_active":true,"app_source":"self_service","trust_level":"trusted","trust_score":97.5,"report_count":7,"public_report_count":2,"verified_report_count":5,"quarantined_report_count":1,"rolled_back_report_count":0,"last_report_at":"2026-03-09 08:33:39","last_rollback_at":"2026-03-09 09:10:11"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "secret-key")
	client.HTTP = server.Client()

	got, err := client.GetMe()
	if err != nil {
		t.Fatalf("GetMe: %v", err)
	}
	if got == nil {
		t.Fatal("GetMe returned nil response")
	}
	if got.LastReportAt == nil || !got.LastReportAt.Equal(time.Date(2026, 3, 9, 8, 33, 39, 0, time.UTC)) {
		t.Fatalf("LastReportAt = %v, want 2026-03-09 08:33:39 UTC", got.LastReportAt)
	}
	if got.LastRollbackAt == nil || !got.LastRollbackAt.Equal(time.Date(2026, 3, 9, 9, 10, 11, 0, time.UTC)) {
		t.Fatalf("LastRollbackAt = %v, want 2026-03-09 09:10:11 UTC", got.LastRollbackAt)
	}
}

func TestGetMeReturnsErrorOnUnexpectedStatus(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	client := NewClient(server.URL, "secret-key")
	client.HTTP = server.Client()

	got, err := client.GetMe()
	if err == nil {
		t.Fatal("GetMe error = nil, want error")
	}
	if got != nil {
		t.Fatalf("GetMe response = %+v, want nil", got)
	}
}

type stubKeyStore struct {
	raw      map[string][]byte
	setCalls int
	setErr   error
}

func (s *stubKeyStore) Get(key string, target interface{}) (bool, error) {
	raw, ok := s.raw[key]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(raw, target)
}

func (s *stubKeyStore) Set(key string, value interface{}) error {
	if s.setErr != nil {
		return s.setErr
	}
	if s.raw == nil {
		s.raw = make(map[string][]byte)
	}
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	s.raw[key] = b
	s.setCalls++
	return nil
}

func TestRegisterKeyPostsAppNameAndDecodesResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %q, want %q", r.Method, http.MethodPost)
		}
		if r.URL.Path != "/api/v1/keys/roll_key" {
			t.Fatalf("path = %q, want %q", r.URL.Path, "/api/v1/keys/roll_key")
		}
		var req KeyCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("Decode request: %v", err)
		}
		if req.Name != DefaultAppName {
			t.Fatalf("name = %q, want %q", req.Name, DefaultAppName)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"app-1","name":"StreamNZB","token":"new-token","created_at":"2026-03-09T12:34:56Z"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "")
	client.HTTP = server.Client()

	got, err := client.RegisterKey(DefaultAppName)
	if err != nil {
		t.Fatalf("RegisterKey: %v", err)
	}
	if got == nil {
		t.Fatal("RegisterKey returned nil response")
	}
	if got.Token != "new-token" {
		t.Fatalf("unexpected response: %+v", got)
	}
}

func TestRegisterKeyDecodesNumericID(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":456,"name":"StreamNZB","token":"new-token","created_at":"2026-03-09T12:34:56Z"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "")
	client.HTTP = server.Client()

	got, err := client.RegisterKey(DefaultAppName)
	if err != nil {
		t.Fatalf("RegisterKey: %v", err)
	}
	if got == nil {
		t.Fatal("RegisterKey returned nil response")
	}
	if got.ID != "456" {
		t.Fatalf("ID = %q, want %q", got.ID, "456")
	}
}

func TestRegisterKeyReturnsErrorWhenIPAlreadyHasKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"message":"An AvailNZB key already exists for your IP address"}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "")
	client.HTTP = server.Client()

	got, err := client.RegisterKey(DefaultAppName)
	if err == nil {
		t.Fatal("RegisterKey error = nil, want error")
	}
	if got != nil {
		t.Fatalf("RegisterKey response = %+v, want nil", got)
	}
	if !errors.Is(err, ErrRegisterKeyIPAlreadyHasKey) {
		t.Fatalf("RegisterKey error = %v, want ErrRegisterKeyIPAlreadyHasKey", err)
	}
}

func TestRegisterAndPersistRecoversKeyWhenIPAlreadyHasKey(t *testing.T) {
	store := &stubKeyStore{raw: map[string][]byte{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == apiPath+"/keys/recover" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":5,"name":"StreamNZB","token":"recovered-token","is_active":true,"rotated_at":"2026-03-15T10:05:00Z"}`))
			return
		}
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"This IP already has a key"}`))
	}))
	defer server.Close()

	got, err := RegisterAndPersistAPIKey(store, server.URL, DefaultAppName)
	if err != nil {
		t.Fatalf("RegisterAndPersistAPIKey error = %v, want nil", err)
	}
	if got != "recovered-token" {
		t.Fatalf("RegisterAndPersistAPIKey token = %q, want %q", got, "recovered-token")
	}
	if store.setCalls != 1 {
		t.Fatalf("setCalls = %d, want 1", store.setCalls)
	}

	var saved apiKeyState
	if found, err := store.Get(apiKeyStateKey, &saved); err != nil {
		t.Fatalf("Get persisted key: %v", err)
	} else if !found {
		t.Fatal("persisted key not found")
	}
	if saved.ID != "5" || saved.Token != "recovered-token" {
		t.Fatalf("unexpected persisted state: %+v", saved)
	}
}

func TestResolveAPIKeyReturnsExplicitOverride(t *testing.T) {
	t.Parallel()

	got, err := ResolveAPIKey(nil, "https://snzb.stream", "configured-key", DefaultAppName)
	if err != nil {
		t.Fatalf("ResolveAPIKey: %v", err)
	}
	if got != "configured-key" {
		t.Fatalf("ResolveAPIKey() = %q, want %q", got, "configured-key")
	}
}

func TestResolveAPIKeyReturnsStoredKeyWhenHealthy(t *testing.T) {
	t.Parallel()

	store := &stubKeyStore{raw: map[string][]byte{apiKeyStateKey: []byte(`"stored-token"`)}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/me" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("X-API-Key"); got != "stored-token" {
			t.Fatalf("X-API-Key = %q, want %q", got, "stored-token")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"app-1","name":"StreamNZB","is_active":true,"app_source":"self_service","trust_level":"trusted","trust_score":97.5,"report_count":7,"public_report_count":2,"verified_report_count":5,"quarantined_report_count":1,"rolled_back_report_count":0,"last_report_at":null,"last_rollback_at":null}`))
	}))
	defer server.Close()

	got, err := ResolveAPIKey(store, server.URL, "", DefaultAppName)
	if err != nil {
		t.Fatalf("ResolveAPIKey: %v", err)
	}
	if got != "stored-token" {
		t.Fatalf("ResolveAPIKey() = %q, want %q", got, "stored-token")
	}
	if store.setCalls != 0 {
		t.Fatalf("setCalls = %d, want 0", store.setCalls)
	}
}

func TestResolveAPIKeyScenarios(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		stored              string
		meStatus            int
		recoverStatus       int
		registerStatus      int
		registerBody        string
		recoverBody         string
		wantToken           string
		wantRecoverCalls    int
		wantRegisterCalls   int
		wantRecoverHasToken bool
	}{
		{
			name:              "No key, roll new key",
			stored:            "",
			recoverStatus:     http.StatusUnauthorized,
			registerStatus:    http.StatusCreated,
			registerBody:      `{"id":"2","name":"StreamNZB","token":"rolled-token","created_at":"2026-03-09T13:00:00Z"}`,
			wantToken:         "rolled-token",
			wantRecoverCalls:  1,
			wantRegisterCalls: 1,
		},
		{
			name:                "Key wrong ip, reroll",
			stored:              "old-token",
			meStatus:            http.StatusUnauthorized,
			recoverStatus:       http.StatusOK,
			recoverBody:         `{"id":"9","name":"StreamNZB","token":"recovered-token","is_active":true,"rotated_at":"2026-03-09T13:10:00Z"}`,
			wantToken:           "recovered-token",
			wantRecoverCalls:    1,
			wantRecoverHasToken: true,
		},
		{
			name:              "Wrong key ip, reroll",
			stored:            "bad-key",
			meStatus:          http.StatusUnauthorized,
			recoverStatus:     http.StatusUnauthorized,
			registerStatus:    http.StatusForbidden,
			registerBody:      `{"error":"This IP already has a key"}`,
			recoverBody:       `{"id":"11","name":"StreamNZB","token":"ip-recovered-token","is_active":true,"rotated_at":"2026-03-09T13:11:00Z"}`,
			wantToken:         "ip-recovered-token",
			wantRecoverCalls:  2,
			wantRegisterCalls: 0,
		},
		{
			name:                "wrong key wrong ip, roll new key",
			stored:              "bad-key",
			meStatus:            http.StatusUnauthorized,
			recoverStatus:       http.StatusUnauthorized,
			registerStatus:      http.StatusCreated,
			registerBody:        `{"id":"3","name":"StreamNZB","token":"new-roll-token","created_at":"2026-03-09T13:20:00Z"}`,
			wantToken:           "new-roll-token",
			wantRecoverCalls:    2,
			wantRegisterCalls:   1,
			wantRecoverHasToken: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			store := &stubKeyStore{raw: map[string][]byte{}}
			if tt.stored != "" {
				store.raw[apiKeyStateKey] = []byte(`"` + tt.stored + `"`)
			}

			recoverCalls := 0
			registerCalls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/v1/me":
					status := tt.meStatus
					if status == 0 {
						status = http.StatusOK
					}
					w.WriteHeader(status)
					if status == http.StatusOK {
						_, _ = w.Write([]byte(`{"id":"app-1","name":"StreamNZB","is_active":true,"app_source":"self_service","trust_level":"trusted","trust_score":97.5,"report_count":7,"public_report_count":2,"verified_report_count":5,"quarantined_report_count":1,"rolled_back_report_count":0,"last_report_at":null,"last_rollback_at":null}`))
					} else {
						_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
					}
				case r.Method == http.MethodPost && r.URL.Path == "/api/v1/keys/recover":
					recoverCalls++
					body := map[string]string{}
					_ = json.NewDecoder(r.Body).Decode(&body)
					hasToken := body["token"] != ""
					if tt.wantRecoverHasToken && recoverCalls == 1 && !hasToken {
						t.Fatalf("expected first recover request token in body")
					}
					status := tt.recoverStatus
					if status == 0 {
						status = http.StatusOK
					}
					if tt.name == "Wrong key ip, reroll" && recoverCalls == 2 && !hasToken {
						status = http.StatusOK
					}
					w.WriteHeader(status)
					if status == http.StatusOK {
						_, _ = w.Write([]byte(tt.recoverBody))
					} else {
						_, _ = w.Write([]byte(`{"error":"recover failed"}`))
					}
				case r.Method == http.MethodPost && r.URL.Path == "/api/v1/keys/roll_key":
					registerCalls++
					status := tt.registerStatus
					if status == 0 {
						status = http.StatusCreated
					}
					w.WriteHeader(status)
					_, _ = w.Write([]byte(tt.registerBody))
				default:
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
			}))
			defer server.Close()

			got, err := ResolveAPIKey(store, server.URL, "", DefaultAppName)
			if err != nil {
				t.Fatalf("ResolveAPIKey: %v", err)
			}
			if got != tt.wantToken {
				t.Fatalf("ResolveAPIKey() = %q, want %q", got, tt.wantToken)
			}
			if recoverCalls != tt.wantRecoverCalls {
				t.Fatalf("recoverCalls = %d, want %d", recoverCalls, tt.wantRecoverCalls)
			}
			if registerCalls != tt.wantRegisterCalls {
				t.Fatalf("registerCalls = %d, want %d", registerCalls, tt.wantRegisterCalls)
			}
		})
	}
}

func TestResolveStartupAPIKeyReturnsEmptyWhenMissingWithoutRegistering(t *testing.T) {
	t.Parallel()

	store := &stubKeyStore{raw: map[string][]byte{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("unexpected registration request")
	}))
	defer server.Close()

	got, err := ResolveStartupAPIKey(store, server.URL, "")
	if err != nil {
		t.Fatalf("ResolveStartupAPIKey: %v", err)
	}
	if got != "" {
		t.Fatalf("ResolveStartupAPIKey() = %q, want empty string", got)
	}
	if store.setCalls != 0 {
		t.Fatalf("setCalls = %d, want 0", store.setCalls)
	}
}

func TestResolveAPIKeyRegistersAndPersistsWhenMissing(t *testing.T) {
	t.Parallel()

	store := &stubKeyStore{raw: map[string][]byte{}}
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/keys/recover" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"recover failed"}`))
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/v1/keys/roll_key" {
			callCount++
			var req KeyCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("Decode request: %v", err)
			}
			if req.Name != DefaultAppName {
				t.Fatalf("name = %q, want %q", req.Name, DefaultAppName)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":2,"name":"StreamNZB","token":"registered-token","created_at":"2026-03-09T13:00:00Z"}`))
			return
		}
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	got, err := ResolveAPIKey(store, server.URL, "", DefaultAppName)
	if err != nil {
		t.Fatalf("ResolveAPIKey: %v", err)
	}
	if got != "registered-token" {
		t.Fatalf("ResolveAPIKey() = %q, want %q", got, "registered-token")
	}
	if callCount != 1 {
		t.Fatalf("register call count = %d, want 1", callCount)
	}

	var saved apiKeyState
	if found, err := store.Get(apiKeyStateKey, &saved); err != nil {
		t.Fatalf("Get persisted key: %v", err)
	} else if !found {
		t.Fatal("persisted key not found")
	}
	if saved.ID != "2" || saved.Token != "registered-token" {
		t.Fatalf("unexpected persisted state: %+v", saved)
	}
}

func TestClientSetAPIKeyIsUsedBySubsequentRequests(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-API-Key"); got != "updated-key" {
			t.Fatalf("X-API-Key = %q, want %q", got, "updated-key")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"app-1","name":"StreamNZB","is_active":true,"app_source":"self_service","trust_level":"trusted","trust_score":97.5,"report_count":7,"public_report_count":2,"verified_report_count":5,"quarantined_report_count":1,"rolled_back_report_count":0,"last_report_at":null,"last_rollback_at":null}`))
	}))
	defer server.Close()

	client := NewClient(server.URL, "")
	client.HTTP = server.Client()
	client.SetAPIKey(" updated-key ")

	if got := client.GetAPIKey(); got != "updated-key" {
		t.Fatalf("GetAPIKey() = %q, want %q", got, "updated-key")
	}

	resp, err := client.GetMe()
	if err != nil {
		t.Fatalf("GetMe: %v", err)
	}
	if resp == nil {
		t.Fatal("GetMe returned nil response")
	}
}

// The report carries poster and usenet_date so AvailNZB can mint the Warden
// fingerprint server-side. StreamNZB never hashes anything itself: a shared
// pool only works when every participant hashes byte-for-byte identically, and
// one implementation on the server cannot drift from itself.
func TestReportAvailabilitySendsPosterAndUsenetDate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		poster         string
		usenetDate     int64
		wantPoster     string
		wantUsenetDate float64
	}{
		{"both present", "someone@example.com", 1781151834, "someone@example.com", 1781151834},
		// One alone mints nothing, so neither half is sent on its own.
		{"poster only", "someone@example.com", 0, "", 0},
		{"usenet date only", "", 1781151834, "", 0},
		{"neither", "", 0, "", 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var body map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/report" {
					t.Errorf("path = %q, want %q", r.URL.Path, "/api/v1/report")
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode report body: %v", err)
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer server.Close()

			client := NewClient(server.URL, "key")
			client.HTTP = server.Client()

			err := client.ReportAvailability("https://indexer/details/a", "news.example.com", true, ReportMeta{
				ReleaseName: "Movie.2160p.Remux-GRP",
				Size:        1234,
				ImdbID:      "tt1234567",
				Poster:      tt.poster,
				UsenetDate:  tt.usenetDate,
			})
			if err != nil {
				t.Fatalf("ReportAvailability: %v", err)
			}

			gotPoster, _ := body["poster"].(string)
			if gotPoster != tt.wantPoster {
				t.Errorf("poster = %v, want %q", body["poster"], tt.wantPoster)
			}
			gotDate, _ := body["usenet_date"].(float64)
			if gotDate != tt.wantUsenetDate {
				t.Errorf("usenet_date = %v, want %v", body["usenet_date"], tt.wantUsenetDate)
			}
			// Both fields are omitempty, so an unreportable pair leaves the
			// payload byte-for-byte what it is today.
			if tt.wantPoster == "" {
				if _, ok := body["poster"]; ok {
					t.Errorf("poster present in payload, want omitted")
				}
			}
			if tt.wantUsenetDate == 0 {
				if _, ok := body["usenet_date"]; ok {
					t.Errorf("usenet_date present in payload, want omitted")
				}
			}
		})
	}
}

// A warden block is absent for every release with no stored fingerprint, which
// is every release reported before poster and usenet date were sent. Absent
// means "nothing known" — it decodes to nil and nothing changes, because
// available already accounts for any verdict that is present.
func TestGetStatusDecodesWardenAndFailsOpenWithoutIt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		payload       string
		wantAvailable bool
		wantWarden    *Warden
	}{
		{
			name:          "no warden block",
			payload:       `{"url":"https://indexer/a","available":true,"summary":{"omicron":{"text":"ok","healthy":true,"last_updated":"2026-06-08T12:00:00Z"}}}`,
			wantAvailable: true,
		},
		{
			name:          "dead verdict",
			payload:       `{"url":"https://indexer/a","available":false,"summary":{},"warden":{"dead":true,"global":false,"backbones":["omicron"],"sources":2,"dead_at":1785447545}}`,
			wantAvailable: false,
			wantWarden:    &Warden{Dead: true, Backbones: []string{"omicron"}, Sources: 2, DeadAt: 1785447545},
		},
		{
			name:          "alive verdict leaves availability alone",
			payload:       `{"url":"https://indexer/a","available":true,"summary":{},"warden":{"dead":false,"global":false,"sources":3,"dead_at":0}}`,
			wantAvailable: true,
			wantWarden:    &Warden{Sources: 3},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.payload))
			}))
			defer server.Close()

			client := NewClient(server.URL, "key")
			client.HTTP = server.Client()

			status, err := client.GetStatus("https://indexer/a", nil)
			if err != nil {
				t.Fatalf("GetStatus: %v", err)
			}
			if status == nil {
				t.Fatal("GetStatus returned nil status")
			}
			if status.Available != tt.wantAvailable {
				t.Errorf("Available = %v, want %v", status.Available, tt.wantAvailable)
			}
			if tt.wantWarden == nil {
				if status.Warden != nil {
					t.Fatalf("Warden = %+v, want nil", status.Warden)
				}
				return
			}
			if status.Warden == nil {
				t.Fatal("Warden = nil, want a verdict")
			}
			if !reflect.DeepEqual(status.Warden, tt.wantWarden) {
				t.Errorf("Warden = %+v, want %+v", status.Warden, tt.wantWarden)
			}
		})
	}
}

// Verdicts are scoped server-side to the backbones behind the hostnames we
// name, because AvailNZB's exact-host map resolves reseller subdomains that a
// root-domain heuristic gets wrong. An unscoped lookup would report a release
// dead on a backbone we cannot even reach.
func TestGetStatusScopesToProviders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		providers     []string
		wantProviders string
		wantPresent   bool
	}{
		{"scoped to our hosts", []string{"news.example.com", "newshosting.tweaknews.eu"}, "news.example.com,newshosting.tweaknews.eu", true},
		{"no hosts leaves the param off", nil, "", false},
		{"empty slice leaves the param off", []string{}, "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var query url.Values
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				query = r.URL.Query()
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"url":"https://indexer/a","available":true,"summary":{}}`))
			}))
			defer server.Close()

			client := NewClient(server.URL, "key")
			client.HTTP = server.Client()

			if _, err := client.GetStatus("https://indexer/a", tt.providers); err != nil {
				t.Fatalf("GetStatus: %v", err)
			}
			if got := query.Get("url"); got != "https://indexer/a" {
				t.Errorf("url = %q, want %q", got, "https://indexer/a")
			}
			if _, present := query["providers"]; present != tt.wantPresent {
				t.Fatalf("providers present = %v, want %v", present, tt.wantPresent)
			}
			if got := query.Get("providers"); got != tt.wantProviders {
				t.Errorf("providers = %q, want %q", got, tt.wantProviders)
			}
		})
	}
}

// CheckPreDownload is the gate playback actually consults. A release whose
// status carries no warden block has to come back exactly as it does today —
// a missing verdict may never block a release.
func TestCheckPreDownloadIgnoresAMissingWardenBlock(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/backbones":
			_, _ = w.Write([]byte(`{"backbones":["omicron"],"provider_hostnames":{"omicron":["news.example.com"]}}`))
		default:
			if got := r.URL.Query().Get("providers"); got != "news.example.com" {
				t.Errorf("providers = %q, want %q", got, "news.example.com")
			}
			_, _ = w.Write([]byte(`{"url":"https://indexer/a","available":true,"summary":{"omicron":{"text":"ok","healthy":true,"last_updated":"2026-06-08T12:00:00Z"}}}`))
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, "key")
	client.HTTP = server.Client()
	if err := client.RefreshBackbones(); err != nil {
		t.Fatalf("RefreshBackbones: %v", err)
	}

	available, _, capableProvider, err := client.CheckPreDownload("https://indexer/a", []string{"news.example.com"})
	if err != nil {
		t.Fatalf("CheckPreDownload: %v", err)
	}
	if !available {
		t.Error("available = false, want true: a release with no verdict must fail open")
	}
	if capableProvider != "news.example.com" {
		t.Errorf("capableProvider = %q, want %q", capableProvider, "news.example.com")
	}
}
