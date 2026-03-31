package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"streamnzb/pkg/core/config"
	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/persistence"
	"strings"
	"sync"
)

func ptrBool(v bool) *bool { return &v }

func parseTrailingNumber(value string) (int, bool) {
	value = strings.TrimSpace(value)
	end := len(value)
	for end > 0 && value[end-1] >= '0' && value[end-1] <= '9' {
		end--
	}
	if end == len(value) {
		return 0, false
	}
	n, err := strconv.Atoi(value[end:])
	if err != nil {
		return 0, false
	}
	return n, true
}

type Device struct {
	Username            string                                `json:"username"`
	Token               string                                `json:"token"`
	Order               int                                   `json:"order,omitempty"`
	FilterSortingMode   string                                `json:"filter_sorting_mode,omitempty"`
	IndexerMode         string                                `json:"indexer_mode,omitempty"`
	UseAvailNZB         *bool                                 `json:"use_availnzb,omitempty"`
	CombineResults      *bool                                 `json:"combine_results,omitempty"`
	EnableFailover      *bool                                 `json:"enable_failover,omitempty"`
	ResultsMode         string                                `json:"results_mode,omitempty"`
	IndexerOverrides    map[string]config.IndexerSearchConfig `json:"indexer_overrides"`
	ProviderSelections  []string                              `json:"provider_selections,omitempty"`
	IndexerSelections   []string                              `json:"indexer_selections,omitempty"`
	MovieSearchQueries  []string                              `json:"movie_search_queries,omitempty"`
	SeriesSearchQueries []string                              `json:"series_search_queries,omitempty"`
}

// Stream is the stream-model name for the legacy Device struct.
// Keep the JSON shape unchanged while newer code moves to stream terminology.
type Stream = Device

type DeviceManager struct {
	mu      sync.RWMutex
	devices map[string]*Device
	manager *persistence.StateManager
	cfg     *config.Config
	saveFn  func() error
}

// StreamManager is the stream-model name for the legacy DeviceManager type.
// It remains an alias so existing persistence and API code keep working.
type StreamManager = DeviceManager

var globalDeviceManager *DeviceManager
var deviceManagerMu sync.Mutex

func GetDeviceManager(dataDir string) (*DeviceManager, error) {
	deviceManagerMu.Lock()
	defer deviceManagerMu.Unlock()

	if globalDeviceManager != nil {
		return globalDeviceManager, nil
	}

	manager, err := persistence.GetManager(dataDir)
	if err != nil {
		return nil, fmt.Errorf("failed to get persistence manager: %w", err)
	}

	dm := &DeviceManager{
		devices: make(map[string]*Device),
		manager: manager,
	}

	if err := dm.load(); err != nil {
		return nil, fmt.Errorf("failed to load devices: %w", err)
	}

	globalDeviceManager = dm
	return dm, nil
}

// GetStreamManager returns the shared stream manager for the given data directory.
func GetStreamManager(dataDir string) (*StreamManager, error) {
	return GetDeviceManager(dataDir)
}

func NewDeviceManagerFromConfig(cfg *config.Config, saveFn func() error) (*DeviceManager, error) {
	deviceManagerMu.Lock()
	defer deviceManagerMu.Unlock()

	if globalDeviceManager != nil {
		return globalDeviceManager, nil
	}

	if cfg.Devices == nil {
		cfg.Devices = make(map[string]*config.DeviceEntry)
	}

	dm := &DeviceManager{
		devices: make(map[string]*Device),
		cfg:     cfg,
		saveFn:  saveFn,
	}
	if err := dm.load(); err != nil {
		return nil, fmt.Errorf("failed to load devices from config: %w", err)
	}
	globalDeviceManager = dm
	return dm, nil
}

// NewStreamManagerFromConfig creates the shared stream manager backed by config persistence.
func NewStreamManagerFromConfig(cfg *config.Config, saveFn func() error) (*StreamManager, error) {
	return NewDeviceManagerFromConfig(cfg, saveFn)
}

func (dm *DeviceManager) load() error {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	if dm.cfg != nil {
		dm.devices = make(map[string]*Device)
		if dm.cfg.Devices != nil {
			for k, e := range dm.cfg.Devices {
				if e == nil {
					continue
				}
				ov := e.IndexerOverrides
				if ov == nil {
					ov = make(map[string]config.IndexerSearchConfig)
				}
				dm.devices[k] = &Device{
					Username:            e.Username,
					Token:               e.Token,
					Order:               e.Order,
					FilterSortingMode:   e.FilterSortingMode,
					IndexerMode:         e.IndexerMode,
					UseAvailNZB:         e.UseAvailNZB,
					CombineResults:      e.CombineResults,
					EnableFailover:      e.EnableFailover,
					ResultsMode:         e.ResultsMode,
					IndexerOverrides:    ov,
					ProviderSelections:  append([]string(nil), e.ProviderSelections...),
					IndexerSelections:   append([]string(nil), e.IndexerSelections...),
					MovieSearchQueries:  append([]string(nil), e.MovieSearchQueries...),
					SeriesSearchQueries: append([]string(nil), e.SeriesSearchQueries...),
				}
			}
		}
		if _, exists := dm.devices["admin"]; exists {
			delete(dm.devices, "admin")
			dm.saveLocked()
			logger.Info("Removed legacy admin from devices (admin is in config)")
		}
		return nil
	}

	var devices map[string]*Device
	found, err := dm.manager.Get("devices", &devices)
	if err != nil {
		return err
	}
	if !found {
		var users map[string]*Device
		if found, err := dm.manager.Get("users", &users); found && err == nil {
			devices = users
			dm.manager.Set("devices", devices)
			logger.Info("Migrated users to devices in state.json")
		}
	}
	if devices != nil {
		dm.devices = make(map[string]*Device)
		for k, d := range devices {
			if d == nil {
				continue
			}
			dm.devices[k] = &Device{
				Username:            d.Username,
				Token:               d.Token,
				Order:               d.Order,
				FilterSortingMode:   d.FilterSortingMode,
				IndexerMode:         d.IndexerMode,
				UseAvailNZB:         d.UseAvailNZB,
				CombineResults:      d.CombineResults,
				EnableFailover:      d.EnableFailover,
				ResultsMode:         d.ResultsMode,
				IndexerOverrides:    d.IndexerOverrides,
				ProviderSelections:  append([]string(nil), d.ProviderSelections...),
				IndexerSelections:   append([]string(nil), d.IndexerSelections...),
				MovieSearchQueries:  append([]string(nil), d.MovieSearchQueries...),
				SeriesSearchQueries: append([]string(nil), d.SeriesSearchQueries...),
			}
			if dm.devices[k].IndexerOverrides == nil {
				dm.devices[k].IndexerOverrides = make(map[string]config.IndexerSearchConfig)
			}
		}
		if _, exists := dm.devices["admin"]; exists {
			delete(dm.devices, "admin")
			dm.saveLocked()
			logger.Info("Removed legacy admin from devices (admin is in config)")
		}
	} else {
		dm.devices = make(map[string]*Device)
	}
	return nil
}

func (dm *DeviceManager) saveLocked() error {
	if dm.cfg != nil {
		dm.cfg.Devices = make(map[string]*config.DeviceEntry)
		for k, d := range dm.devices {
			ov := d.IndexerOverrides
			if ov == nil {
				ov = make(map[string]config.IndexerSearchConfig)
			}
			dm.cfg.Devices[k] = &config.DeviceEntry{
				Username:            d.Username,
				Token:               d.Token,
				Order:               d.Order,
				FilterSortingMode:   d.FilterSortingMode,
				IndexerMode:         d.IndexerMode,
				UseAvailNZB:         d.UseAvailNZB,
				CombineResults:      d.CombineResults,
				EnableFailover:      d.EnableFailover,
				ResultsMode:         d.ResultsMode,
				IndexerOverrides:    ov,
				ProviderSelections:  append([]string(nil), d.ProviderSelections...),
				IndexerSelections:   append([]string(nil), d.IndexerSelections...),
				MovieSearchQueries:  append([]string(nil), d.MovieSearchQueries...),
				SeriesSearchQueries: append([]string(nil), d.SeriesSearchQueries...),
			}
		}
		return dm.cfg.Save()
	}
	return dm.manager.Set("devices", dm.devices)
}

func (dm *DeviceManager) SetConfig(cfg *config.Config, saveFn func() error) {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	dm.cfg = cfg
	dm.saveFn = saveFn
	if dm.cfg != nil && dm.cfg.Devices == nil {
		dm.cfg.Devices = make(map[string]*config.DeviceEntry)
	}
}

func HashPassword(password string) string {
	hash := sha256.Sum256([]byte(password))
	return hex.EncodeToString(hash[:])
}

func GenerateToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	hash := sha256.Sum256(bytes)
	return hex.EncodeToString(hash[:]), nil
}

func (dm *DeviceManager) Authenticate(loginUsername, password, adminUsername, adminPasswordHash, adminToken string) (*Device, error) {
	if adminUsername == "" {
		adminUsername = "admin"
	}

	if loginUsername == adminUsername {
		if adminPasswordHash == "" || adminToken == "" {
			return nil, fmt.Errorf("invalid credentials")
		}
		passwordHash := HashPassword(password)
		if passwordHash != adminPasswordHash {
			return nil, fmt.Errorf("invalid credentials")
		}
		return &Device{
			Username:         adminUsername,
			Token:            adminToken,
			IndexerOverrides: nil,
		}, nil
	}

	return nil, fmt.Errorf("invalid credentials")
}

func (dm *DeviceManager) AuthenticateToken(token string, adminUsername, adminToken string) (*Device, error) {
	if adminUsername == "" {
		adminUsername = "admin"
	}

	if adminToken != "" && token == adminToken {
		return &Device{
			Username:         adminUsername,
			Token:            adminToken,
			IndexerOverrides: nil,
		}, nil
	}

	dm.mu.RLock()
	defer dm.mu.RUnlock()
	for _, device := range dm.devices {
		if device.Token == token {
			return device, nil
		}
	}

	return nil, fmt.Errorf("invalid token")
}

func (dm *DeviceManager) GetDevice(username string, adminUsername string) (*Device, error) {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	if adminUsername == "" {
		adminUsername = "admin"
	}
	if username == adminUsername {
		return nil, fmt.Errorf("admin is not a regular device")
	}

	device, exists := dm.devices[username]
	if !exists {
		return nil, fmt.Errorf("device not found")
	}

	return device, nil
}

func (dm *DeviceManager) GetUser(username string, adminUsername string) (*Device, error) {
	return dm.GetDevice(username, adminUsername)
}

func (dm *DeviceManager) GetStream(username string, adminUsername string) (*Stream, error) {
	return dm.GetDevice(username, adminUsername)
}

func (dm *DeviceManager) GetAllDevices() []Device {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	devices := make([]Device, 0, len(dm.devices))
	for _, device := range dm.devices {

		if device.Username == "admin" {
			continue
		}
		devices = append(devices, Device{
			Username:            device.Username,
			Token:               device.Token,
			Order:               device.Order,
			FilterSortingMode:   device.FilterSortingMode,
			IndexerMode:         device.IndexerMode,
			UseAvailNZB:         device.UseAvailNZB,
			CombineResults:      device.CombineResults,
			EnableFailover:      device.EnableFailover,
			ResultsMode:         device.ResultsMode,
			IndexerOverrides:    device.IndexerOverrides,
			ProviderSelections:  append([]string(nil), device.ProviderSelections...),
			IndexerSelections:   append([]string(nil), device.IndexerSelections...),
			MovieSearchQueries:  append([]string(nil), device.MovieSearchQueries...),
			SeriesSearchQueries: append([]string(nil), device.SeriesSearchQueries...),
		})
	}

	sort.Slice(devices, func(i, j int) bool {
		left := devices[i]
		right := devices[j]

		if left.Order > 0 && right.Order > 0 && left.Order != right.Order {
			return left.Order < right.Order
		}
		if left.Order > 0 && right.Order <= 0 {
			return true
		}
		if left.Order <= 0 && right.Order > 0 {
			return false
		}

		leftNum, leftHasNum := parseTrailingNumber(left.Username)
		rightNum, rightHasNum := parseTrailingNumber(right.Username)
		if leftHasNum && rightHasNum && leftNum != rightNum {
			return leftNum < rightNum
		}
		if !strings.EqualFold(left.Username, right.Username) {
			return strings.ToLower(left.Username) < strings.ToLower(right.Username)
		}
		return left.Username < right.Username
	})

	return devices
}

func (dm *DeviceManager) GetAllStreams() []Stream {
	return dm.GetAllDevices()
}

func (dm *DeviceManager) nextDeviceOrderLocked() int {
	maxOrder := 0
	for _, device := range dm.devices {
		if device != nil && device.Order > maxOrder {
			maxOrder = device.Order
		}
	}
	return maxOrder + 1
}

func (dm *DeviceManager) GetAllUsers() []Device {
	return dm.GetAllDevices()
}

func (dm *DeviceManager) CreateDevice(username, password string, adminUsername string) (*Device, error) {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	if adminUsername == "" {
		adminUsername = "admin"
	}
	if username == adminUsername {
		return nil, fmt.Errorf("cannot create admin device via this method")
	}

	if _, exists := dm.devices[username]; exists {
		return nil, fmt.Errorf("device already exists")
	}

	token, err := GenerateToken()
	if err != nil {
		return nil, fmt.Errorf("failed to generate token: %w", err)
	}

	device := &Device{
		Username:            username,
		Token:               token,
		Order:               dm.nextDeviceOrderLocked(),
		IndexerMode:         "combine",
		UseAvailNZB:         ptrBool(true),
		CombineResults:      ptrBool(true),
		IndexerOverrides:    make(map[string]config.IndexerSearchConfig),
		ProviderSelections:  []string{},
		IndexerSelections:   []string{},
		MovieSearchQueries:  []string{},
		SeriesSearchQueries: []string{},
	}

	dm.devices[username] = device

	if err := dm.saveLocked(); err != nil {
		delete(dm.devices, username)
		return nil, fmt.Errorf("failed to save device: %w", err)
	}

	logger.Info("Created device", "username", username)
	return device, nil
}

func (dm *DeviceManager) CreateUser(username, password string, adminUsername string) (*Device, error) {
	return dm.CreateDevice(username, password, adminUsername)
}

func (dm *DeviceManager) CreateStream(username, password string, adminUsername string) (*Stream, error) {
	return dm.CreateDevice(username, password, adminUsername)
}

func (dm *DeviceManager) UpdateUser(username, newPassword string, adminUsername string) error {
	if adminUsername == "" {
		adminUsername = "admin"
	}
	if username == adminUsername {
		return fmt.Errorf("admin password is managed via config; use dashboard to change")
	}
	return fmt.Errorf("only admin password can be updated")
}

func (dm *DeviceManager) RegenerateToken(username string) (string, error) {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	device, exists := dm.devices[username]
	if !exists {
		return "", fmt.Errorf("device not found")
	}

	token, err := GenerateToken()
	if err != nil {
		return "", fmt.Errorf("failed to generate token: %w", err)
	}

	device.Token = token

	if err := dm.saveLocked(); err != nil {
		return "", fmt.Errorf("failed to save device: %w", err)
	}

	logger.Info("Regenerated token for device", "username", username)
	return token, nil
}

func (dm *DeviceManager) DeleteDevice(username string) error {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	if _, exists := dm.devices[username]; !exists {
		return fmt.Errorf("device not found")
	}

	delete(dm.devices, username)

	if err := dm.saveLocked(); err != nil {
		return fmt.Errorf("failed to save device: %w", err)
	}

	logger.Info("Deleted device", "username", username)
	return nil
}

func (dm *DeviceManager) DeleteUser(username string) error {
	return dm.DeleteDevice(username)
}

func (dm *DeviceManager) DeleteStream(username string) error {
	return dm.DeleteDevice(username)
}

func (dm *DeviceManager) UpdateDeviceIndexerConfig(username string, selections []string, overrides map[string]config.IndexerSearchConfig) error {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	device, exists := dm.devices[username]
	if !exists {
		return fmt.Errorf("device not found")
	}

	if overrides == nil {
		device.IndexerOverrides = make(map[string]config.IndexerSearchConfig)
	} else {
		device.IndexerOverrides = overrides
	}
	device.IndexerSelections = append([]string(nil), selections...)

	if err := dm.saveLocked(); err != nil {
		return fmt.Errorf("failed to save device indexer overrides: %w", err)
	}
	return nil
}

func (dm *DeviceManager) UpdateStreamIndexerConfig(username string, selections []string, overrides map[string]config.IndexerSearchConfig) error {
	return dm.UpdateDeviceIndexerConfig(username, selections, overrides)
}

func (dm *DeviceManager) UpdateDeviceSearchQueries(username string, movieSearchQueries, seriesSearchQueries []string) error {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	device, exists := dm.devices[username]
	if !exists {
		return fmt.Errorf("device not found")
	}

	device.MovieSearchQueries = append([]string(nil), movieSearchQueries...)
	device.SeriesSearchQueries = append([]string(nil), seriesSearchQueries...)

	if err := dm.saveLocked(); err != nil {
		return fmt.Errorf("failed to save device search queries: %w", err)
	}
	return nil
}

func (dm *DeviceManager) UpdateStreamSearchQueries(username string, movieSearchQueries, seriesSearchQueries []string) error {
	return dm.UpdateDeviceSearchQueries(username, movieSearchQueries, seriesSearchQueries)
}

func (dm *DeviceManager) UpdateDeviceProviderSelections(username string, providerSelections []string) error {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	device, exists := dm.devices[username]
	if !exists {
		return fmt.Errorf("device not found")
	}

	device.ProviderSelections = append([]string(nil), providerSelections...)

	if err := dm.saveLocked(); err != nil {
		return fmt.Errorf("failed to save device provider selections: %w", err)
	}
	return nil
}

func (dm *DeviceManager) UpdateStreamProviderSelections(username string, providerSelections []string) error {
	return dm.UpdateDeviceProviderSelections(username, providerSelections)
}

func (dm *DeviceManager) UpdateDeviceGeneralSettings(username, filterSortingMode, indexerMode string, useAvailNZB, combineResults, enableFailover *bool, resultsMode string) error {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	device, exists := dm.devices[username]
	if !exists {
		return fmt.Errorf("device not found")
	}

	device.FilterSortingMode = strings.TrimSpace(filterSortingMode)
	device.IndexerMode = strings.TrimSpace(indexerMode)
	device.UseAvailNZB = useAvailNZB
	device.CombineResults = combineResults
	device.EnableFailover = enableFailover
	device.ResultsMode = strings.TrimSpace(resultsMode)

	if err := dm.saveLocked(); err != nil {
		return fmt.Errorf("failed to save device general settings: %w", err)
	}
	return nil
}

func (dm *DeviceManager) UpdateStreamGeneralSettings(username, filterSortingMode, indexerMode string, useAvailNZB, combineResults, enableFailover *bool, resultsMode string) error {
	return dm.UpdateDeviceGeneralSettings(username, filterSortingMode, indexerMode, useAvailNZB, combineResults, enableFailover, resultsMode)
}

// UpdateStreamConfig persists the full stream configuration in a single save.
func (dm *DeviceManager) UpdateStreamConfig(username string, streamConfig *Device) error {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	device, exists := dm.devices[username]
	if !exists {
		return fmt.Errorf("device not found")
	}
	if streamConfig == nil {
		return fmt.Errorf("stream config is required")
	}

	device.FilterSortingMode = strings.TrimSpace(streamConfig.FilterSortingMode)
	device.IndexerMode = strings.TrimSpace(streamConfig.IndexerMode)
	device.UseAvailNZB = streamConfig.UseAvailNZB
	device.CombineResults = streamConfig.CombineResults
	device.EnableFailover = streamConfig.EnableFailover
	device.ResultsMode = strings.TrimSpace(streamConfig.ResultsMode)
	if streamConfig.IndexerOverrides == nil {
		device.IndexerOverrides = make(map[string]config.IndexerSearchConfig)
	} else {
		device.IndexerOverrides = streamConfig.IndexerOverrides
	}
	device.ProviderSelections = append([]string(nil), streamConfig.ProviderSelections...)
	device.IndexerSelections = append([]string(nil), streamConfig.IndexerSelections...)
	device.MovieSearchQueries = append([]string(nil), streamConfig.MovieSearchQueries...)
	device.SeriesSearchQueries = append([]string(nil), streamConfig.SeriesSearchQueries...)

	if err := dm.saveLocked(); err != nil {
		return fmt.Errorf("failed to save stream config: %w", err)
	}
	return nil
}
