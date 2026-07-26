package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
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

type Stream struct {
	Username            string                                `json:"username"`
	Token               string                                `json:"token"`
	Order               int                                   `json:"order,omitempty"`
	FilterSortingMode   string                                `json:"filter_sorting_mode,omitempty"`
	IndexerMode         string                                `json:"indexer_mode,omitempty"`
	UseAvailNZB         *bool                                 `json:"use_availnzb,omitempty"`
	CombineResults      *bool                                 `json:"combine_results,omitempty"`
	EnableFailover      *bool                                 `json:"enable_failover,omitempty"`
	ResultsMode         string                                `json:"results_mode,omitempty"`
	AutoAddProviders    *bool                                 `json:"auto_add_providers,omitempty"`
	AutoAddIndexers     *bool                                 `json:"auto_add_indexers,omitempty"`
	IndexerOverrides    map[string]config.IndexerSearchConfig `json:"indexer_overrides"`
	ProviderSelections  []string                              `json:"provider_selections,omitempty"`
	IndexerSelections   []string                              `json:"indexer_selections,omitempty"`
	MovieSearchQueries  []string                              `json:"movie_search_queries,omitempty"`
	SeriesSearchQueries []string                              `json:"series_search_queries,omitempty"`
	FilterProfileName   string                                `json:"filter_profile_name,omitempty"`
	FilterProfileByType map[string]string                     `json:"filter_profile_by_type,omitempty"`
	MuteErrorVideo      *bool                                 `json:"mute_error_video,omitempty"`
}

func (s *Stream) IsErrorVideoMuted(cfg *config.Config) bool {
	if s != nil && s.MuteErrorVideo != nil {
		return *s.MuteErrorVideo
	}
	if cfg != nil {
		return cfg.IsErrorVideoMuted()
	}
	return false
}

type StreamManager struct {
	mu      sync.RWMutex
	streams map[string]*Stream
	manager *persistence.StateManager
	cfg     *config.Config
	saveFn  func() error
}

var globalStreamManager *StreamManager
var streamManagerMu sync.Mutex

func GetStreamManager(dataDir string) (*StreamManager, error) {
	streamManagerMu.Lock()
	defer streamManagerMu.Unlock()

	if globalStreamManager != nil {
		return globalStreamManager, nil
	}

	manager, err := persistence.GetManager(dataDir)
	if err != nil {
		return nil, fmt.Errorf("failed to get persistence manager: %w", err)
	}

	dm := &StreamManager{
		streams: make(map[string]*Stream),
		manager: manager,
	}

	if err := dm.load(); err != nil {
		return nil, fmt.Errorf("failed to load streams: %w", err)
	}

	globalStreamManager = dm
	return dm, nil
}

// NewStreamManagerFromConfig creates the shared stream manager backed by config persistence.
func NewStreamManagerFromConfig(cfg *config.Config, saveFn func() error) (*StreamManager, error) {
	streamManagerMu.Lock()
	defer streamManagerMu.Unlock()

	if globalStreamManager != nil {
		return globalStreamManager, nil
	}

	if cfg.Streams == nil {
		cfg.Streams = make(map[string]*config.StreamEntry)
	}

	dm := &StreamManager{
		streams: make(map[string]*Stream),
		cfg:     cfg,
		saveFn:  saveFn,
	}
	if err := dm.load(); err != nil {
		return nil, fmt.Errorf("failed to load streams from config: %w", err)
	}
	globalStreamManager = dm
	return dm, nil
}

func (dm *StreamManager) load() error {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	if dm.cfg != nil {
		removedAdmin := dm.syncStreamsFromConfigLocked()
		if removedAdmin {
			if err := dm.saveLocked(); err != nil {
				logger.Warn("Failed to persist removal of legacy admin stream", "err", err)
			}
		}
		return nil
	}

	var devices map[string]*Stream
	found, err := dm.manager.Get("devices", &devices)
	if err != nil {
		return err
	}
	if !found {
		var users map[string]*Stream
		if found, err := dm.manager.Get("users", &users); found && err == nil {
			devices = users
			dm.manager.Set("devices", devices)
			logger.Info("Migrated legacy stream entries in state.json")
		}
	}
	if devices != nil {
		dm.streams = make(map[string]*Stream)
		for k, d := range devices {
			if d == nil {
				continue
			}
			dm.streams[k] = &Stream{
				Username:            d.Username,
				Token:               d.Token,
				Order:               d.Order,
				FilterSortingMode:   d.FilterSortingMode,
				IndexerMode:         d.IndexerMode,
				UseAvailNZB:         d.UseAvailNZB,
				CombineResults:      d.CombineResults,
				EnableFailover:      d.EnableFailover,
				ResultsMode:         d.ResultsMode,
				AutoAddProviders:    d.AutoAddProviders,
				AutoAddIndexers:     d.AutoAddIndexers,
				IndexerOverrides:    d.IndexerOverrides,
				ProviderSelections:  append([]string(nil), d.ProviderSelections...),
				IndexerSelections:   append([]string(nil), d.IndexerSelections...),
				MovieSearchQueries:  append([]string(nil), d.MovieSearchQueries...),
				SeriesSearchQueries: append([]string(nil), d.SeriesSearchQueries...),
			}
			if dm.streams[k].IndexerOverrides == nil {
				dm.streams[k].IndexerOverrides = make(map[string]config.IndexerSearchConfig)
			}
		}
		if _, exists := dm.streams["admin"]; exists {
			delete(dm.streams, "admin")
			dm.saveLocked()
			logger.Info("Removed legacy admin from streams (admin is in config)")
		}
	} else {
		dm.streams = make(map[string]*Stream)
	}
	return nil
}

func (dm *StreamManager) syncStreamsFromConfigLocked() bool {
	dm.streams = make(map[string]*Stream)
	if dm.cfg == nil || dm.cfg.Streams == nil {
		return false
	}
	for k, e := range dm.cfg.Streams {
		if e == nil {
			continue
		}
		ov := e.IndexerOverrides
		if ov == nil {
			ov = make(map[string]config.IndexerSearchConfig)
		}
		dm.streams[k] = &Stream{
			Username:            e.Username,
			Token:               e.Token,
			Order:               e.Order,
			FilterSortingMode:   e.FilterSortingMode,
			IndexerMode:         e.IndexerMode,
			UseAvailNZB:         e.UseAvailNZB,
			CombineResults:      e.CombineResults,
			EnableFailover:      e.EnableFailover,
			ResultsMode:         e.ResultsMode,
			AutoAddProviders:    e.AutoAddProviders,
			AutoAddIndexers:     e.AutoAddIndexers,
			IndexerOverrides:    ov,
			ProviderSelections:  append([]string(nil), e.ProviderSelections...),
			IndexerSelections:   append([]string(nil), e.IndexerSelections...),
			MovieSearchQueries:  append([]string(nil), e.MovieSearchQueries...),
			SeriesSearchQueries: append([]string(nil), e.SeriesSearchQueries...),
			FilterProfileName:   e.FilterProfileName,
			FilterProfileByType: maps.Clone(e.FilterProfileByType),
			MuteErrorVideo:      e.MuteErrorVideo,
		}
	}
	if _, exists := dm.streams["admin"]; exists {
		delete(dm.streams, "admin")
		logger.Info("Removed legacy admin from streams (admin is in config)")
		return true
	}
	return false
}

func (dm *StreamManager) saveLocked() error {
	if dm.cfg != nil {
		dm.cfg.Streams = make(map[string]*config.StreamEntry)
		for k, d := range dm.streams {
			ov := d.IndexerOverrides
			if ov == nil {
				ov = make(map[string]config.IndexerSearchConfig)
			}
			dm.cfg.Streams[k] = &config.StreamEntry{
				Username:            d.Username,
				Token:               d.Token,
				Order:               d.Order,
				FilterSortingMode:   d.FilterSortingMode,
				IndexerMode:         d.IndexerMode,
				UseAvailNZB:         d.UseAvailNZB,
				CombineResults:      d.CombineResults,
				EnableFailover:      d.EnableFailover,
				ResultsMode:         d.ResultsMode,
				AutoAddProviders:    d.AutoAddProviders,
				AutoAddIndexers:     d.AutoAddIndexers,
				IndexerOverrides:    ov,
				ProviderSelections:  append([]string(nil), d.ProviderSelections...),
				IndexerSelections:   append([]string(nil), d.IndexerSelections...),
				MovieSearchQueries:  append([]string(nil), d.MovieSearchQueries...),
				SeriesSearchQueries: append([]string(nil), d.SeriesSearchQueries...),
				FilterProfileName:   d.FilterProfileName,
				FilterProfileByType: maps.Clone(d.FilterProfileByType),
				MuteErrorVideo:      d.MuteErrorVideo,
			}
		}
		return dm.cfg.Save()
	}
	return dm.manager.Set("devices", dm.streams)
}

func (dm *StreamManager) SetConfig(cfg *config.Config, saveFn func() error) {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	dm.cfg = cfg
	dm.saveFn = saveFn
	if dm.cfg != nil && dm.cfg.Streams == nil {
		dm.cfg.Streams = make(map[string]*config.StreamEntry)
	}
	if dm.cfg != nil {
		dm.syncStreamsFromConfigLocked()
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

func (dm *StreamManager) Authenticate(loginUsername, password, adminUsername, adminPasswordHash, adminToken string) (*Stream, error) {
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
		return &Stream{
			Username:         adminUsername,
			Token:            adminToken,
			IndexerOverrides: nil,
		}, nil
	}

	return nil, fmt.Errorf("invalid credentials")
}

func (dm *StreamManager) AuthenticateToken(token string, adminUsername, adminToken string) (*Stream, error) {
	if adminUsername == "" {
		adminUsername = "admin"
	}

	if adminToken != "" && token == adminToken {
		return &Stream{
			Username:         adminUsername,
			Token:            adminToken,
			IndexerOverrides: nil,
		}, nil
	}

	dm.mu.RLock()
	defer dm.mu.RUnlock()
	for _, stream := range dm.streams {
		if stream.Token == token {
			return stream, nil
		}
	}

	return nil, fmt.Errorf("invalid token")
}

func (dm *StreamManager) GetStream(username string, adminUsername string) (*Stream, error) {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	if adminUsername == "" {
		adminUsername = "admin"
	}
	if username == adminUsername {
		return nil, fmt.Errorf("admin is not a regular stream")
	}

	stream, exists := dm.streams[username]
	if !exists {
		return nil, fmt.Errorf("stream not found")
	}

	return stream, nil
}

func (dm *StreamManager) GetAllStreams() []Stream {
	dm.mu.RLock()
	defer dm.mu.RUnlock()

	streams := make([]Stream, 0, len(dm.streams))
	for _, stream := range dm.streams {

		if stream.Username == "admin" {
			continue
		}
		streams = append(streams, Stream{
			Username:            stream.Username,
			Token:               stream.Token,
			Order:               stream.Order,
			FilterSortingMode:   stream.FilterSortingMode,
			IndexerMode:         stream.IndexerMode,
			UseAvailNZB:         stream.UseAvailNZB,
			CombineResults:      stream.CombineResults,
			EnableFailover:      stream.EnableFailover,
			ResultsMode:         stream.ResultsMode,
			AutoAddProviders:    stream.AutoAddProviders,
			AutoAddIndexers:     stream.AutoAddIndexers,
			IndexerOverrides:    stream.IndexerOverrides,
			ProviderSelections:  append([]string(nil), stream.ProviderSelections...),
			IndexerSelections:   append([]string(nil), stream.IndexerSelections...),
			MovieSearchQueries:  append([]string(nil), stream.MovieSearchQueries...),
			SeriesSearchQueries: append([]string(nil), stream.SeriesSearchQueries...),
			FilterProfileName:   stream.FilterProfileName,
			FilterProfileByType: maps.Clone(stream.FilterProfileByType),
			MuteErrorVideo:      stream.MuteErrorVideo,
		})
	}

	sort.Slice(streams, func(i, j int) bool {
		left := streams[i]
		right := streams[j]

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

	return streams
}

func (dm *StreamManager) nextStreamOrderLocked() int {
	maxOrder := 0
	for _, stream := range dm.streams {
		if stream != nil && stream.Order > maxOrder {
			maxOrder = stream.Order
		}
	}
	return maxOrder + 1
}

func (dm *StreamManager) CreateStream(username, password string, adminUsername string) (*Stream, error) {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	if adminUsername == "" {
		adminUsername = "admin"
	}
	if username == adminUsername {
		return nil, fmt.Errorf("cannot create admin stream via this method")
	}

	if _, exists := dm.streams[username]; exists {
		return nil, fmt.Errorf("stream already exists")
	}

	token, err := GenerateToken()
	if err != nil {
		return nil, fmt.Errorf("failed to generate token: %w", err)
	}

	stream := &Stream{
		Username:            username,
		Token:               token,
		Order:               dm.nextStreamOrderLocked(),
		IndexerMode:         "combine",
		UseAvailNZB:         ptrBool(true),
		CombineResults:      ptrBool(true),
		AutoAddProviders:    ptrBool(true),
		AutoAddIndexers:     ptrBool(true),
		IndexerOverrides:    make(map[string]config.IndexerSearchConfig),
		ProviderSelections:  []string{},
		IndexerSelections:   []string{},
		MovieSearchQueries:  []string{},
		SeriesSearchQueries: []string{},
		FilterProfileName:   "",
	}

	dm.streams[username] = stream

	if err := dm.saveLocked(); err != nil {
		delete(dm.streams, username)
		return nil, fmt.Errorf("failed to save stream: %w", err)
	}

	logger.Info("Created stream", "username", username)
	return stream, nil
}

func (dm *StreamManager) RegenerateToken(username string) (string, error) {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	stream, exists := dm.streams[username]
	if !exists {
		return "", fmt.Errorf("stream not found")
	}

	token, err := GenerateToken()
	if err != nil {
		return "", fmt.Errorf("failed to generate token: %w", err)
	}

	stream.Token = token

	if err := dm.saveLocked(); err != nil {
		return "", fmt.Errorf("failed to save stream: %w", err)
	}

	logger.Info("Regenerated token for stream", "username", username)
	return token, nil
}

func (dm *StreamManager) DeleteStream(username string) error {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	if _, exists := dm.streams[username]; !exists {
		return fmt.Errorf("stream not found")
	}

	delete(dm.streams, username)

	if err := dm.saveLocked(); err != nil {
		return fmt.Errorf("failed to save stream: %w", err)
	}

	logger.Info("Deleted stream", "username", username)
	return nil
}

func (dm *StreamManager) UpdateStreamIndexerConfig(username string, selections []string, overrides map[string]config.IndexerSearchConfig) error {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	stream, exists := dm.streams[username]
	if !exists {
		return fmt.Errorf("stream not found")
	}

	if overrides == nil {
		stream.IndexerOverrides = make(map[string]config.IndexerSearchConfig)
	} else {
		stream.IndexerOverrides = overrides
	}
	stream.IndexerSelections = append([]string(nil), selections...)

	if err := dm.saveLocked(); err != nil {
		return fmt.Errorf("failed to save stream indexer overrides: %w", err)
	}
	return nil
}

func (dm *StreamManager) UpdateStreamSearchQueries(username string, movieSearchQueries, seriesSearchQueries []string) error {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	stream, exists := dm.streams[username]
	if !exists {
		return fmt.Errorf("stream not found")
	}

	stream.MovieSearchQueries = append([]string(nil), movieSearchQueries...)
	stream.SeriesSearchQueries = append([]string(nil), seriesSearchQueries...)

	if err := dm.saveLocked(); err != nil {
		return fmt.Errorf("failed to save stream search queries: %w", err)
	}
	return nil
}

func (dm *StreamManager) UpdateStreamProviderSelections(username string, providerSelections []string) error {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	stream, exists := dm.streams[username]
	if !exists {
		return fmt.Errorf("stream not found")
	}

	stream.ProviderSelections = append([]string(nil), providerSelections...)

	if err := dm.saveLocked(); err != nil {
		return fmt.Errorf("failed to save stream provider selections: %w", err)
	}
	return nil
}

func (dm *StreamManager) UpdateStreamGeneralSettings(username, filterSortingMode, indexerMode string, useAvailNZB, combineResults, enableFailover *bool, resultsMode string) error {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	stream, exists := dm.streams[username]
	if !exists {
		return fmt.Errorf("stream not found")
	}

	stream.FilterSortingMode = strings.TrimSpace(filterSortingMode)
	stream.IndexerMode = strings.TrimSpace(indexerMode)
	stream.UseAvailNZB = useAvailNZB
	stream.CombineResults = combineResults
	stream.EnableFailover = enableFailover
	stream.ResultsMode = strings.TrimSpace(resultsMode)

	if err := dm.saveLocked(); err != nil {
		return fmt.Errorf("failed to save stream general settings: %w", err)
	}
	return nil
}

// UpdateStreamConfig persists the full stream configuration in a single save.
func (dm *StreamManager) UpdateStreamConfig(username string, streamConfig *Stream) error {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	stream, exists := dm.streams[username]
	if !exists {
		return fmt.Errorf("stream not found")
	}
	if streamConfig == nil {
		return fmt.Errorf("stream config is required")
	}

	stream.FilterSortingMode = strings.TrimSpace(streamConfig.FilterSortingMode)
	stream.IndexerMode = strings.TrimSpace(streamConfig.IndexerMode)
	stream.UseAvailNZB = streamConfig.UseAvailNZB
	stream.CombineResults = streamConfig.CombineResults
	stream.EnableFailover = streamConfig.EnableFailover
	stream.ResultsMode = strings.TrimSpace(streamConfig.ResultsMode)
	stream.AutoAddProviders = streamConfig.AutoAddProviders
	stream.AutoAddIndexers = streamConfig.AutoAddIndexers
	if streamConfig.IndexerOverrides == nil {
		stream.IndexerOverrides = make(map[string]config.IndexerSearchConfig)
	} else {
		stream.IndexerOverrides = streamConfig.IndexerOverrides
	}
	stream.ProviderSelections = append([]string(nil), streamConfig.ProviderSelections...)
	stream.IndexerSelections = append([]string(nil), streamConfig.IndexerSelections...)
	stream.MovieSearchQueries = append([]string(nil), streamConfig.MovieSearchQueries...)
	stream.SeriesSearchQueries = append([]string(nil), streamConfig.SeriesSearchQueries...)
	stream.FilterProfileName = strings.TrimSpace(streamConfig.FilterProfileName)
	stream.MuteErrorVideo = streamConfig.MuteErrorVideo

	if err := dm.saveLocked(); err != nil {
		return fmt.Errorf("failed to save stream config: %w", err)
	}
	return nil
}
