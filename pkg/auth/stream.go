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
	Username           string   `json:"username"`
	Token              string   `json:"token"`
	Order              int      `json:"order,omitempty"`
	FilterSortingMode  string   `json:"filter_sorting_mode,omitempty"`
	IndexerMode        string   `json:"indexer_mode,omitempty"`
	UseAvailNZB        *bool    `json:"use_availnzb,omitempty"`
	FilterAvailNZB     *bool    `json:"filter_availnzb,omitempty"`
	CombineResults     *bool    `json:"combine_results,omitempty"`
	EnableFailover     *bool    `json:"enable_failover,omitempty"`
	ResultsMode        string   `json:"results_mode,omitempty"`
	AutoAddProviders   *bool    `json:"auto_add_providers,omitempty"`
	AutoAddIndexers    *bool    `json:"auto_add_indexers,omitempty"`
	ProviderSelections []string `json:"provider_selections,omitempty"`
	// ProviderConnectionLimits caps this stream's concurrent connections per
	// provider during playback. See config.StreamEntry; the two structs are
	// converted into each other, so their fields must stay in step.
	ProviderConnectionLimits map[string]int                        `json:"provider_connection_limits,omitempty"`
	IndexerSelections        []string                              `json:"indexer_selections,omitempty"`
	IndexerOverrides         map[string]config.IndexerSearchConfig `json:"indexer_overrides"`
	MovieSearchQueries       []string                              `json:"movie_search_queries,omitempty"`
	SeriesSearchQueries      []string                              `json:"series_search_queries,omitempty"`
	FilterProfileName        string                                `json:"filter_profile_name,omitempty"`
	FilterProfileByType      map[string]string                     `json:"filter_profile_by_type,omitempty"`
	MuteErrorVideo           *bool                                 `json:"mute_error_video,omitempty"`
	// ResultNameTemplate and ResultDescriptionTemplate customize how this
	// stream's Stremio results render. Empty uses the built-in format.
	ResultNameTemplate        string `json:"result_name_template,omitempty"`
	ResultDescriptionTemplate string `json:"result_description_template,omitempty"`
}

func (s *Stream) EffectiveFilterAvailNZB(cfg *config.Config) bool {
	if cfg != nil && config.NormalizeAvailNZBMode(cfg.AvailNZBMode) == "off" {
		return false
	}
	if s == nil || s.FilterAvailNZB == nil {
		return false
	}
	return *s.FilterAvailNZB
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
			copied := *d
			copied.deepen()
			dm.streams[k] = &copied
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

// Stream and config.StreamEntry declare the same fields in the same order,
// which makes them directly convertible (struct tags are ignored in
// conversions). That is deliberate: adding a field to one without the other is
// a compile error, which is what stops the two from drifting — an earlier
// hand-written copy in load() silently dropped five fields. Keep both
// declarations in the same order.
var _ = func() struct{} {
	var e config.StreamEntry
	_ = Stream(e)
	return struct{}{}
}()

// deepen replaces shared slices and maps with copies so a converted Stream
// never aliases the config it came from.
func (s *Stream) deepen() {
	s.ProviderSelections = append([]string(nil), s.ProviderSelections...)
	s.ProviderConnectionLimits = maps.Clone(s.ProviderConnectionLimits)
	s.IndexerSelections = append([]string(nil), s.IndexerSelections...)
	s.MovieSearchQueries = append([]string(nil), s.MovieSearchQueries...)
	s.SeriesSearchQueries = append([]string(nil), s.SeriesSearchQueries...)
	s.FilterProfileByType = maps.Clone(s.FilterProfileByType)
	if s.IndexerOverrides == nil {
		// A non-nil overrides map doubles as the selected-indexer set at search
		// time, so it must never be nil on a live Stream.
		s.IndexerOverrides = make(map[string]config.IndexerSearchConfig)
	} else {
		s.IndexerOverrides = maps.Clone(s.IndexerOverrides)
	}
}

// streamFromEntry converts a persisted config entry into a runtime Stream.
func streamFromEntry(e *config.StreamEntry) *Stream {
	s := Stream(*e)
	s.deepen()
	return &s
}

// streamToEntry converts a runtime Stream into its persisted form.
func streamToEntry(s *Stream) *config.StreamEntry {
	clone := *s
	clone.deepen()
	e := config.StreamEntry(clone)
	return &e
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
		dm.streams[k] = streamFromEntry(e)
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
			dm.cfg.Streams[k] = streamToEntry(d)
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
		copied := *stream
		copied.deepen()
		streams = append(streams, copied)
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

// mutate applies fn to the named stream under the write lock and persists the
// result. what labels the save error, e.g. "stream provider selections".
func (dm *StreamManager) mutate(username, what string, fn func(*Stream)) error {
	dm.mu.Lock()
	defer dm.mu.Unlock()

	stream, exists := dm.streams[username]
	if !exists {
		return fmt.Errorf("stream not found")
	}
	fn(stream)
	if err := dm.saveLocked(); err != nil {
		return fmt.Errorf("failed to save %s: %w", what, err)
	}
	return nil
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
	return dm.mutate(username, "stream indexer overrides", func(stream *Stream) {
		if overrides == nil {
			stream.IndexerOverrides = make(map[string]config.IndexerSearchConfig)
		} else {
			stream.IndexerOverrides = overrides
		}
		stream.IndexerSelections = append([]string(nil), selections...)
	})
}

func (dm *StreamManager) UpdateStreamSearchQueries(username string, movieSearchQueries, seriesSearchQueries []string) error {
	return dm.mutate(username, "stream search queries", func(stream *Stream) {
		stream.MovieSearchQueries = append([]string(nil), movieSearchQueries...)
		stream.SeriesSearchQueries = append([]string(nil), seriesSearchQueries...)
	})
}

func (dm *StreamManager) UpdateStreamProviderSelections(username string, providerSelections []string) error {
	return dm.mutate(username, "stream provider selections", func(stream *Stream) {
		stream.ProviderSelections = append([]string(nil), providerSelections...)
	})
}

func (dm *StreamManager) UpdateStreamGeneralSettings(username, filterSortingMode, indexerMode string, useAvailNZB, combineResults, enableFailover *bool, resultsMode string) error {
	return dm.mutate(username, "stream general settings", func(stream *Stream) {
		stream.FilterSortingMode = strings.TrimSpace(filterSortingMode)
		stream.IndexerMode = strings.TrimSpace(indexerMode)
		stream.UseAvailNZB = useAvailNZB
		stream.CombineResults = combineResults
		stream.EnableFailover = enableFailover
		stream.ResultsMode = strings.TrimSpace(resultsMode)
	})
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
	stream.FilterAvailNZB = streamConfig.FilterAvailNZB
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
	stream.ProviderConnectionLimits = maps.Clone(streamConfig.ProviderConnectionLimits)
	stream.IndexerSelections = append([]string(nil), streamConfig.IndexerSelections...)
	stream.MovieSearchQueries = append([]string(nil), streamConfig.MovieSearchQueries...)
	stream.SeriesSearchQueries = append([]string(nil), streamConfig.SeriesSearchQueries...)
	stream.FilterProfileName = strings.TrimSpace(streamConfig.FilterProfileName)
	stream.FilterProfileByType = normalizeProfileBindings(streamConfig.FilterProfileByType)
	stream.MuteErrorVideo = streamConfig.MuteErrorVideo
	stream.ResultNameTemplate = strings.TrimSpace(streamConfig.ResultNameTemplate)
	stream.ResultDescriptionTemplate = strings.TrimSpace(streamConfig.ResultDescriptionTemplate)

	if err := dm.saveLocked(); err != nil {
		return fmt.Errorf("failed to save stream config: %w", err)
	}
	return nil
}

// normalizeProfileBindings trims a stream's per-content-kind profile bindings
// and drops the empty ones, so a kind cleared in the UI is removed rather than
// stored as a binding to nothing. Returns nil when nothing is bound, keeping
// it out of the saved config entirely.
func normalizeProfileBindings(bindings map[string]string) map[string]string {
	if len(bindings) == 0 {
		return nil
	}
	out := make(map[string]string, len(bindings))
	for kind, name := range bindings {
		kind = strings.ToLower(strings.TrimSpace(kind))
		name = strings.TrimSpace(name)
		if kind == "" || name == "" {
			continue
		}
		out[kind] = name
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
