package config

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"streamnzb/pkg/core/logger"
)

// DataStore persists small runtime state (tokens) to a JSON-encoded .dat file.
// User configuration lives in .env; DataStore is for internal data only.
type DataStore struct {
	mu   sync.Mutex
	path string
	data dataStoreData
}

type dataStoreData struct {
	AdminToken         string          `json:"admin_token,omitempty"`
	TVDBToken          string          `json:"tvdb_token,omitempty"`
	TVDBTokenCreatedAt string          `json:"tvdb_token_created_at,omitempty"`
	AvailNZBKeyState   json.RawMessage `json:"availnzb_key_state,omitempty"`
}

// NewDataStore loads (or creates) the .dat file at path.
func NewDataStore(path string) *DataStore {
	ds := &DataStore{path: path}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &ds.data); err != nil {
			logger.Warn("Failed to parse data store, starting fresh", "path", path, "err", err)
		}
	}
	return ds
}

func (ds *DataStore) save() error {
	raw, err := json.MarshalIndent(&ds.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(ds.path, raw, 0644)
}

// AdminToken returns the stored admin token.
func (ds *DataStore) AdminToken() string {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	return ds.data.AdminToken
}

// SetAdminToken persists a new admin token.
func (ds *DataStore) SetAdminToken(token string) error {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	ds.data.AdminToken = token
	return ds.save()
}

// LoadToken returns the cached TVDB token and its creation time.
// Implements tvdb.TokenStore.
func (ds *DataStore) LoadToken() (string, time.Time) {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	if ds.data.TVDBToken == "" {
		return "", time.Time{}
	}
	created, err := time.Parse(time.RFC3339, ds.data.TVDBTokenCreatedAt)
	if err != nil {
		return "", time.Time{}
	}
	return ds.data.TVDBToken, created
}

// SaveToken persists a new TVDB token.
// Implements tvdb.TokenStore.
func (ds *DataStore) SaveToken(token string, createdAt time.Time) {
	ds.mu.Lock()
	defer ds.mu.Unlock()
	ds.data.TVDBToken = token
	ds.data.TVDBTokenCreatedAt = createdAt.Format(time.RFC3339)
	if err := ds.save(); err != nil {
		logger.Warn("Failed to save TVDB token to data store", "err", err)
	}
}

// Get retrieves a value from the data store by key, unmarshalling into target.
// Implements availnzb.KeyStore.
func (ds *DataStore) Get(key string, target interface{}) (bool, error) {
	ds.mu.Lock()
	defer ds.mu.Unlock()

	switch key {
	case "availnzb_api_key":
		if len(ds.data.AvailNZBKeyState) == 0 {
			return false, nil
		}
		return true, json.Unmarshal(ds.data.AvailNZBKeyState, target)
	default:
		return false, fmt.Errorf("datastore: unknown key %q", key)
	}
}

// Set stores a value in the data store by key, marshalling from value.
// Implements availnzb.KeyStore.
func (ds *DataStore) Set(key string, value interface{}) error {
	ds.mu.Lock()
	defer ds.mu.Unlock()

	switch key {
	case "availnzb_api_key":
		raw, err := json.Marshal(value)
		if err != nil {
			return fmt.Errorf("datastore: marshal availnzb key state: %w", err)
		}
		ds.data.AvailNZBKeyState = raw
		return ds.save()
	default:
		return fmt.Errorf("datastore: unknown key %q", key)
	}
}

