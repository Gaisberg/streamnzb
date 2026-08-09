package env

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
)

const (
	ADDONPort                      = "ADDON_PORT"
	ADDONBaseURL                   = "ADDON_BASE_URL"
	LOGLevel                       = "LOG_LEVEL"
	LOGPath                        = "LOG_PATH"
	StreamNZBLogPathEnv            = "STREAMNZB_LOG_PATH"
	KeepLogFiles                   = "KEEP_LOG_FILES"
	AvailNZBURL                    = "AVAILNZB_URL"
	AvailNZBAPIKey                 = "AVAILNZB_API_KEY"
	TMDBAPIKey                     = "TMDB_API_KEY"
	TVDBAPIKey                     = "TVDB_API_KEY"
	NNTPProxyPort                  = "NNTP_PROXY_PORT"
	NNTPProxyHost                  = "NNTP_PROXY_HOST"
	NNTPProxyEnabled               = "NNTP_PROXY_ENABLED"
	NNTPProxyAuthUser              = "NNTP_PROXY_AUTH_USER"
	NNTPProxyAuthPass              = "NNTP_PROXY_AUTH_PASS"
	TZVar                          = "TZ"
	ProviderPrefix                 = "PROVIDER_"
	IndexerPrefix                  = "INDEXER_"
	IndexerQueryHeaderEnv          = "INDEXER_QUERY_HEADER"
	IndexerGrabHeaderEnv           = "INDEXER_GRAB_HEADER"
	ProviderHeaderEnv              = "PROVIDER_HEADER"
	StreamNZBIndexerQueryHeaderEnv = "STREAMNZB_INDEXER_QUERY_HEADER"
	StreamNZBIndexerGrabHeaderEnv  = "STREAMNZB_INDEXER_GRAB_HEADER"
	StreamNZBProviderHeaderEnv     = "STREAMNZB_PROVIDER_HEADER"
	ConfigPath                     = "CONFIG_PATH"
	DatabaseDriverEnv              = "DATABASE_DRIVER"
	DatabaseURLEnv                 = "DATABASE_URL"
	StreamNZBDatabaseDriverEnv     = "STREAMNZB_DATABASE_DRIVER"
	StreamNZBDatabaseURLEnv        = "STREAMNZB_DATABASE_URL"
)

const (
	KeyAddonPort          = "addon_port"
	KeyAddonBaseURL       = "addon_base_url"
	KeyLogLevel           = "log_level"
	KeyKeepLogFiles       = "keep_log_files"
	KeyProxyPort          = "proxy_port"
	KeyProxyHost          = "proxy_host"
	KeyProxyEnabled       = "proxy_enabled"
	KeyProxyAuthUser      = "proxy_auth_user"
	KeyProxyAuthPass      = "proxy_auth_pass"
	KeyProviders          = "providers"
	KeyIndexers           = "indexers"
	KeyAvailNZBURL        = "availnzb_url"
	KeyAvailNZBAPIKey     = "availnzb_api_key"
	KeyTMDBAPIKey         = "tmdb_api_key"
	KeyTVDBAPIKey         = "tvdb_api_key"
	KeyIndexerQueryHeader = "indexer_query_header"
	KeyIndexerGrabHeader  = "indexer_grab_header"
	KeyProviderHeader     = "provider_header"
	KeyAdminUsername      = "admin_username"
	KeyAdminMustChangePwd = "admin_must_change_password"
	KeyDatabaseDriver     = "database_driver"
	KeyDatabaseURL        = "database_url"
)

const AdminUsernameEnv = "ADMIN_USERNAME"
const AdminForcePasswordResetEnv = "ADMIN_FORCE_PASSWORD_RESET"

var DefaultIndexerUserAgent = "StreamNZB/dev"
var runtimeHeadersMu sync.RWMutex
var runtimeIndexerQueryHeader = ""
var runtimeIndexerGrabHeader = ""
var runtimeProviderHeader = ""

func TZ() string {
	return os.Getenv(TZVar)
}

func IndexerQueryHeader() string {
	if v := os.Getenv(StreamNZBIndexerQueryHeaderEnv); v != "" {
		return v
	}
	if v := os.Getenv(IndexerQueryHeaderEnv); v != "" {
		return v
	}
	runtimeHeadersMu.RLock()
	defer runtimeHeadersMu.RUnlock()
	if runtimeIndexerQueryHeader != "" {
		return runtimeIndexerQueryHeader
	}
	return DefaultIndexerUserAgent
}

func IndexerGrabHeader() string {
	if v := os.Getenv(StreamNZBIndexerGrabHeaderEnv); v != "" {
		return v
	}
	if v := os.Getenv(IndexerGrabHeaderEnv); v != "" {
		return v
	}
	runtimeHeadersMu.RLock()
	defer runtimeHeadersMu.RUnlock()
	if runtimeIndexerGrabHeader != "" {
		return runtimeIndexerGrabHeader
	}
	return DefaultIndexerUserAgent
}

func ProviderHeader() string {
	if v := os.Getenv(StreamNZBProviderHeaderEnv); v != "" {
		return v
	}
	if v := os.Getenv(ProviderHeaderEnv); v != "" {
		return v
	}
	runtimeHeadersMu.RLock()
	defer runtimeHeadersMu.RUnlock()
	if runtimeProviderHeader != "" {
		return runtimeProviderHeader
	}
	return DefaultIndexerUserAgent
}

func SetRuntimeHeaders(indexerQueryHeader, indexerGrabHeader, providerHeader string) {
	runtimeHeadersMu.Lock()
	defer runtimeHeadersMu.Unlock()
	runtimeIndexerQueryHeader = strings.TrimSpace(indexerQueryHeader)
	runtimeIndexerGrabHeader = strings.TrimSpace(indexerGrabHeader)
	runtimeProviderHeader = strings.TrimSpace(providerHeader)
}

func LogLevel() string {
	if v := os.Getenv(LOGLevel); v != "" {
		return v
	}
	return "INFO"
}

// LogPath is where the log file goes: a file path, or a directory to write
// streamnzb.log into. Empty keeps it in the data directory. Deliberately env
// (and flag) only — it is a deployment concern, not a user setting, so it is
// not part of Config.
func LogPath() string {
	if v := os.Getenv(StreamNZBLogPathEnv); v != "" {
		return v
	}
	return os.Getenv(LOGPath)
}

type Provider struct {
	Name        string
	Host        string
	Port        int
	Username    string
	Password    string
	Connections int
	UseSSL      bool
	Priority    *int
	Enabled     *bool
}

type Indexer struct {
	Name    string
	URL     string
	APIKey  string
	Enabled *bool
}

type ConfigOverrides struct {
	AddonPort          int
	AddonBaseURL       string
	LogLevel           string
	KeepLogFiles       int
	AvailNZBURL        string
	AvailNZBAPIKey     string
	TMDBAPIKey         string
	TVDBAPIKey         string
	IndexerQueryHeader string
	IndexerGrabHeader  string
	ProviderHeader     string
	ProxyPort          int
	ProxyHost          string
	ProxyEnabled       bool
	ProxyAuthUser      string
	ProxyAuthPass      string
	AdminUsername      string
	AdminMustChangePwd bool
	DatabaseDriver     string
	DatabaseURL        string
	Providers          []Provider
	Indexers           []Indexer
}

// envReader accumulates config overrides read from the environment, tracking
// which keys were actually set so callers can tell an env-owned field from an
// unset one.
type envReader struct {
	o    *ConfigOverrides
	keys []string
}

// str assigns the first non-empty value among envVars to dst and records key.
// Multiple envVars support the STREAMNZB_-prefixed name plus its legacy alias.
func (r *envReader) str(dst *string, key string, envVars ...string) {
	for _, name := range envVars {
		if v := os.Getenv(name); v != "" {
			*dst = v
			r.keys = append(r.keys, key)
			return
		}
	}
}

// intVal assigns an int-valued env var to dst when it parses and passes valid.
func (r *envReader) intVal(dst *int, key, envVar string, valid func(int) bool) {
	v := os.Getenv(envVar)
	if v == "" {
		return
	}
	n, err := strconv.Atoi(v)
	if err != nil || (valid != nil && !valid(n)) {
		return
	}
	*dst = n
	r.keys = append(r.keys, key)
}

func ReadConfigOverrides() (ConfigOverrides, []string) {
	var o ConfigOverrides
	r := &envReader{o: &o}

	r.intVal(&o.AddonPort, KeyAddonPort, ADDONPort, nil)
	r.str(&o.AddonBaseURL, KeyAddonBaseURL, ADDONBaseURL)
	r.str(&o.LogLevel, KeyLogLevel, LOGLevel)
	r.intVal(&o.KeepLogFiles, KeyKeepLogFiles, KeepLogFiles, func(n int) bool { return n >= 1 })
	r.str(&o.AvailNZBAPIKey, KeyAvailNZBAPIKey, AvailNZBAPIKey)
	r.str(&o.TMDBAPIKey, KeyTMDBAPIKey, TMDBAPIKey)
	r.str(&o.TVDBAPIKey, KeyTVDBAPIKey, TVDBAPIKey)
	r.str(&o.IndexerQueryHeader, KeyIndexerQueryHeader, StreamNZBIndexerQueryHeaderEnv, IndexerQueryHeaderEnv)
	r.str(&o.IndexerGrabHeader, KeyIndexerGrabHeader, StreamNZBIndexerGrabHeaderEnv, IndexerGrabHeaderEnv)
	r.str(&o.ProviderHeader, KeyProviderHeader, StreamNZBProviderHeaderEnv, ProviderHeaderEnv)
	r.intVal(&o.ProxyPort, KeyProxyPort, NNTPProxyPort, nil)
	r.str(&o.ProxyHost, KeyProxyHost, NNTPProxyHost)
	if v, ok := os.LookupEnv(NNTPProxyEnabled); ok && v != "" {
		o.ProxyEnabled = getEnvBool(NNTPProxyEnabled, true)
		r.keys = append(r.keys, KeyProxyEnabled)
	}
	r.str(&o.ProxyAuthUser, KeyProxyAuthUser, NNTPProxyAuthUser)
	r.str(&o.ProxyAuthPass, KeyProxyAuthPass, NNTPProxyAuthPass)
	r.str(&o.AdminUsername, KeyAdminUsername, AdminUsernameEnv)
	r.str(&o.DatabaseDriver, KeyDatabaseDriver, StreamNZBDatabaseDriverEnv, DatabaseDriverEnv)
	r.str(&o.DatabaseURL, KeyDatabaseURL, StreamNZBDatabaseURLEnv, DatabaseURLEnv)
	if v, ok := os.LookupEnv(AdminForcePasswordResetEnv); ok && v != "" && getEnvBool(AdminForcePasswordResetEnv, false) {
		o.AdminMustChangePwd = true
		r.keys = append(r.keys, KeyAdminMustChangePwd)
	}
	if o.Providers = readProvidersFromEnv(); len(o.Providers) > 0 {
		r.keys = append(r.keys, KeyProviders)
	}
	if o.Indexers = readIndexersFromEnv(); len(o.Indexers) > 0 {
		r.keys = append(r.keys, KeyIndexers)
	}

	return o, r.keys
}

func OverrideKeys() []string {
	_, keys := ReadConfigOverrides()
	return keys
}

func readProvidersFromEnv() []Provider {
	var list []Provider
	for i := 1; i <= 10; i++ {
		prefix := fmt.Sprintf("%s%d_", ProviderPrefix, i)
		host := os.Getenv(prefix + "HOST")
		if host == "" {
			continue
		}
		priority := getEnvInt(prefix+"PRIORITY", i)
		enabled := getEnvBool(prefix+"ENABLED", true)
		list = append(list, Provider{
			Name:        getEnv(prefix+"NAME", fmt.Sprintf("Provider %d", i)),
			Host:        host,
			Port:        getEnvInt(prefix+"PORT", 563),
			Username:    os.Getenv(prefix + "USERNAME"),
			Password:    os.Getenv(prefix + "PASSWORD"),
			Connections: getEnvInt(prefix+"CONNECTIONS", 10),
			UseSSL:      getEnvBool(prefix+"SSL", true),
			Priority:    &priority,
			Enabled:     &enabled,
		})
	}
	return list
}

func readIndexersFromEnv() []Indexer {
	var list []Indexer
	for i := 1; i <= 10; i++ {
		prefix := fmt.Sprintf("%s%d_", IndexerPrefix, i)
		url := os.Getenv(prefix + "URL")
		if url == "" {
			continue
		}
		enabled := getEnvBool(prefix+"ENABLED", true)
		list = append(list, Indexer{
			Name:    getEnv(prefix+"NAME", fmt.Sprintf("Indexer %d", i)),
			URL:     url,
			APIKey:  os.Getenv(prefix + "API_KEY"),
			Enabled: &enabled,
		})
	}
	return list
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return defaultVal
}

func getEnvBool(key string, defaultVal bool) bool {
	if v := os.Getenv(key); v != "" {
		return strings.ToLower(v) == "true" || v == "1"
	}
	return defaultVal
}
