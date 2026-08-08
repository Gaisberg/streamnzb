package availnzb

// This file owns the AvailNZB API-key lifecycle: resolving a key at startup,
// registering a new one, recovering an existing one that is leased to this IP,
// and persisting the result through a KeyStore. The HTTP client itself lives in
// availnzb.go; the transport in transport.go.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"streamnzb/pkg/core/logger"
)

type KeyStore interface {
	Get(key string, target interface{}) (bool, error)
	Set(key string, value interface{}) error
}

type KeyCreateRequest struct {
	Name string `json:"name"`
}

type KeyCreateResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Token     string `json:"token"`
	CreatedAt string `json:"created_at"`
}

type RecoverKeyResponse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Token     string `json:"token"`
	IsActive  bool   `json:"is_active"`
	RotatedAt string `json:"rotated_at"`
}

func (r *RecoverKeyResponse) UnmarshalJSON(data []byte) error {
	type recoverKeyResponseAlias RecoverKeyResponse
	var raw struct {
		recoverKeyResponseAlias
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	id, err := decodeFlexibleString(raw.ID)
	if err != nil {
		return fmt.Errorf("decode recover key response id: %w", err)
	}
	*r = RecoverKeyResponse(raw.recoverKeyResponseAlias)
	r.ID = id
	return nil
}

func (k *KeyCreateResponse) UnmarshalJSON(data []byte) error {
	type keyCreateResponseAlias KeyCreateResponse
	var raw struct {
		keyCreateResponseAlias
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	id, err := decodeFlexibleString(raw.ID)
	if err != nil {
		return fmt.Errorf("decode key create response id: %w", err)
	}
	*k = KeyCreateResponse(raw.keyCreateResponseAlias)
	k.ID = id
	return nil
}

type apiKeyState struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Token     string `json:"token"`
	CreatedAt string `json:"created_at,omitempty"`
}

func ResolveStartupAPIKey(store KeyStore, baseURL, explicitKey string) (string, error) {
	explicitKey = strings.TrimSpace(explicitKey)
	if explicitKey != "" {
		logger.Debug("AvailNZB key bootstrap using explicit API key")
		return explicitKey, nil
	}

	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		logger.Debug("AvailNZB key bootstrap skipped", "reason", "no base URL configured")
		return "", nil
	}
	if store == nil {
		return "", fmt.Errorf("availnzb key bootstrap: store is required")
	}

	storedKey, err := loadStoredAPIKey(store)
	if err != nil {
		return "", err
	}
	if storedKey != "" {
		logger.Debug("AvailNZB key bootstrap using stored API key")
	}
	return storedKey, nil
}

func ResolveAPIKey(store KeyStore, baseURL, explicitKey, appName string) (string, error) {
	if strings.TrimSpace(explicitKey) != "" {
		return strings.TrimSpace(explicitKey), nil
	}
	resolvedKey, err := ResolveStartupAPIKey(store, baseURL, explicitKey)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(baseURL) == "" {
		return resolvedKey, nil
	}
	if resolvedKey != "" {
		client := NewClient(baseURL, resolvedKey)
		if _, err := client.GetMe(); err == nil {
			return resolvedKey, nil
		}
		logger.Info("AvailNZB stored key validation failed, attempting recovery", "err", err)
		recovered, recoverErr := recoverAndPersistAPIKey(store, client)
		if recoverErr == nil && strings.TrimSpace(recovered) != "" {
			return recovered, nil
		}
		logger.Warn("AvailNZB key recovery failed, rotating key", "err", recoverErr)
	}

	return RecoverOrRegisterAndPersistAPIKey(store, baseURL, appName)
}

func RegisterAndPersistAPIKey(store KeyStore, baseURL, appName string) (string, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return "", fmt.Errorf("availnzb register: no base URL configured")
	}
	if store == nil {
		return "", fmt.Errorf("availnzb key bootstrap: store is required")
	}
	if strings.TrimSpace(appName) == "" {
		appName = DefaultAppName
	}

	client := NewClient(baseURL, "")
	created, err := client.RegisterKey(appName)
	if err != nil {
		if errors.Is(err, ErrRegisterKeyIPAlreadyHasKey) {
			logger.Info("AvailNZB key registration refused (IP already has key), attempting recovery")
			return recoverAndPersistAPIKey(store, client)
		}
		return "", err
	}
	if created == nil || strings.TrimSpace(created.Token) == "" {
		return "", fmt.Errorf("availnzb register: empty token in response")
	}

	state := apiKeyState{
		ID:        strings.TrimSpace(created.ID),
		Name:      strings.TrimSpace(created.Name),
		Token:     strings.TrimSpace(created.Token),
		CreatedAt: strings.TrimSpace(created.CreatedAt),
	}
	if err := store.Set(apiKeyStateKey, state); err != nil {
		return "", fmt.Errorf("availnzb key bootstrap: failed to persist API key: %w", err)
	}

	logger.Info("Registered new AvailNZB API key", "name", state.Name, "id", state.ID)
	return state.Token, nil
}

func RecoverOrRegisterAndPersistAPIKey(store KeyStore, baseURL, appName string) (string, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return "", fmt.Errorf("availnzb recover/register: no base URL configured")
	}
	if store == nil {
		return "", fmt.Errorf("availnzb key bootstrap: store is required")
	}
	if strings.TrimSpace(appName) == "" {
		appName = DefaultAppName
	}

	client := NewClient(baseURL, "")
	recovered, recoverErr := recoverAndPersistAPIKey(store, client)
	if recoverErr == nil && strings.TrimSpace(recovered) != "" {
		logger.Info("Recovered AvailNZB API key before rolling a new app key")
		return recovered, nil
	}
	logger.Info("AvailNZB key recovery failed, rolling new key", "err", recoverErr)

	return RegisterAndPersistAPIKey(store, baseURL, appName)
}

func recoverAndPersistAPIKey(store KeyStore, client *Client) (string, error) {
	recovered, err := client.RecoverKey()
	if err != nil {
		return "", fmt.Errorf("availnzb recover: %w", err)
	}
	if recovered == nil || strings.TrimSpace(recovered.Token) == "" {
		return "", fmt.Errorf("availnzb recover: empty token in response")
	}

	state := apiKeyState{
		ID:    strings.TrimSpace(recovered.ID),
		Name:  strings.TrimSpace(recovered.Name),
		Token: strings.TrimSpace(recovered.Token),
	}
	if err := store.Set(apiKeyStateKey, state); err != nil {
		return "", fmt.Errorf("availnzb key bootstrap: failed to persist recovered API key: %w", err)
	}

	logger.Info("Recovered existing AvailNZB API key", "name", state.Name, "id", state.ID)
	return state.Token, nil
}

func loadStoredAPIKey(store KeyStore) (string, error) {
	var state apiKeyState
	found, err := store.Get(apiKeyStateKey, &state)
	if err == nil {
		if found && strings.TrimSpace(state.Token) != "" {
			return strings.TrimSpace(state.Token), nil
		}
		return "", nil
	}

	var legacy string
	legacyFound, legacyErr := store.Get(apiKeyStateKey, &legacy)
	if legacyErr == nil {
		if legacyFound && strings.TrimSpace(legacy) != "" {
			return strings.TrimSpace(legacy), nil
		}
		return "", nil
	}

	return "", fmt.Errorf("availnzb key bootstrap: failed to read stored API key: %w", err)
}

func isRegisterKeyIPAlreadyHasKeyMessage(message string) bool {
	normalized := strings.ToLower(strings.TrimSpace(message))
	if normalized == "" {
		return false
	}
	if strings.Contains(normalized, "already has a key for your ip") || strings.Contains(normalized, "already has a key for this ip") {
		return true
	}
	hasAlready := strings.Contains(normalized, "already") || strings.Contains(normalized, "existing") || strings.Contains(normalized, "exists")
	hasIP := strings.Contains(normalized, " ip") || strings.HasPrefix(normalized, "ip ") || strings.Contains(normalized, "ip address")
	hasKey := strings.Contains(normalized, "key")
	return hasAlready && hasIP && hasKey
}

func isAPIKeyMissingMessage(message string) bool {
	normalized := strings.ToLower(strings.TrimSpace(message))
	if normalized == "" {
		return false
	}
	return strings.Contains(normalized, "x-api-key") && strings.Contains(normalized, "required")
}

func isAPIKeyTemporarilyAssignedMessage(message string) bool {
	normalized := strings.ToLower(strings.TrimSpace(message))
	if normalized == "" {
		return false
	}
	return strings.Contains(normalized, "temporarily assigned to another ip")
}

func registerKeyIPAlreadyHasKeyErr(message string) error {
	message = strings.TrimSpace(message)
	if message == "" {
		return ErrRegisterKeyIPAlreadyHasKey
	}
	return fmt.Errorf("%w: %s", ErrRegisterKeyIPAlreadyHasKey, message)
}

func (c *Client) RegisterKey(name string) (*KeyCreateResponse, error) {
	if c.BaseURL == "" {
		return nil, fmt.Errorf("availnzb register: no base URL configured")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = DefaultAppName
	}

	logger.Debug("AvailNZB RegisterKey", "name", name)

	var created KeyCreateResponse
	err := c.doJSON(context.Background(), requestOptions{
		method:     "POST",
		path:       "/keys/roll_key",
		body:       KeyCreateRequest{Name: name},
		okStatuses: []int{http.StatusOK, http.StatusCreated},
		out:        &created,
		errPrefix:  "availnzb register",
		classifyErr: func(_ int, message string) error {
			if isRegisterKeyIPAlreadyHasKeyMessage(message) {
				return registerKeyIPAlreadyHasKeyErr(message)
			}
			return nil
		},
	})
	if err != nil {
		return nil, err
	}
	return &created, nil
}

func (c *Client) RecoverKey() (*RecoverKeyResponse, error) {
	if c.BaseURL == "" {
		return nil, fmt.Errorf("availnzb recover: no base URL configured")
	}

	logger.Debug("AvailNZB RecoverKey")

	// The current key, when we have one, proves ownership of the lease being
	// recovered; the endpoint also accepts an anonymous (IP-based) recover.
	var body interface{}
	if token := strings.TrimSpace(c.GetAPIKey()); token != "" {
		body = map[string]string{"token": token}
	}

	var recovered RecoverKeyResponse
	if err := c.doJSON(context.Background(), requestOptions{
		method:    "POST",
		path:      "/keys/recover",
		body:      body,
		out:       &recovered,
		errPrefix: "availnzb recover",
	}); err != nil {
		return nil, err
	}
	if id := strings.TrimSpace(recovered.ID); id == "" {
		return nil, fmt.Errorf("availnzb recover: empty id in response")
	}

	return &recovered, nil
}
