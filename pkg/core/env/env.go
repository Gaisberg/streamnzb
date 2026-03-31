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
)

const AdminUsernameEnv = "ADMIN_USERNAME"

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
	Providers          []Provider
	Indexers           []Indexer
}

func ReadConfigOverrides() (ConfigOverrides, []string) {
	var o ConfigOverrides
	var keys []string

	if v := os.Getenv(ADDONPort); v != "" {
		if port, err := strconv.Atoi(v); err == nil {
			o.AddonPort = port
			keys = append(keys, KeyAddonPort)
		}
	}
	if v := os.Getenv(ADDONBaseURL); v != "" {
		o.AddonBaseURL = v
		keys = append(keys, KeyAddonBaseURL)
	}
	if v := os.Getenv(LOGLevel); v != "" {
		o.LogLevel = v
		keys = append(keys, KeyLogLevel)
	}
	if v := os.Getenv(KeepLogFiles); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 {
			o.KeepLogFiles = n
			keys = append(keys, KeyKeepLogFiles)
		}
	}
	if v := os.Getenv(AvailNZBAPIKey); v != "" {
		o.AvailNZBAPIKey = v
		keys = append(keys, KeyAvailNZBAPIKey)
	}
	if v := os.Getenv(TMDBAPIKey); v != "" {
		o.TMDBAPIKey = v
		keys = append(keys, KeyTMDBAPIKey)
	}
	if v := os.Getenv(TVDBAPIKey); v != "" {
		o.TVDBAPIKey = v
		keys = append(keys, KeyTVDBAPIKey)
	}
	if v := os.Getenv(StreamNZBIndexerQueryHeaderEnv); v != "" {
		o.IndexerQueryHeader = v
		keys = append(keys, KeyIndexerQueryHeader)
	} else if v := os.Getenv(IndexerQueryHeaderEnv); v != "" {
		o.IndexerQueryHeader = v
		keys = append(keys, KeyIndexerQueryHeader)
	}
	if v := os.Getenv(StreamNZBIndexerGrabHeaderEnv); v != "" {
		o.IndexerGrabHeader = v
		keys = append(keys, KeyIndexerGrabHeader)
	} else if v := os.Getenv(IndexerGrabHeaderEnv); v != "" {
		o.IndexerGrabHeader = v
		keys = append(keys, KeyIndexerGrabHeader)
	}
	if v := os.Getenv(StreamNZBProviderHeaderEnv); v != "" {
		o.ProviderHeader = v
		keys = append(keys, KeyProviderHeader)
	} else if v := os.Getenv(ProviderHeaderEnv); v != "" {
		o.ProviderHeader = v
		keys = append(keys, KeyProviderHeader)
	}

	if v := os.Getenv(NNTPProxyPort); v != "" {
		if port, err := strconv.Atoi(v); err == nil {
			o.ProxyPort = port
			keys = append(keys, KeyProxyPort)
		}
	}
	if v := os.Getenv(NNTPProxyHost); v != "" {
		o.ProxyHost = v
		keys = append(keys, KeyProxyHost)
	}
	if v, ok := os.LookupEnv(NNTPProxyEnabled); ok && v != "" {
		o.ProxyEnabled = getEnvBool(NNTPProxyEnabled, true)
		keys = append(keys, KeyProxyEnabled)
	}
	if v := os.Getenv(NNTPProxyAuthUser); v != "" {
		o.ProxyAuthUser = v
		keys = append(keys, KeyProxyAuthUser)
	}
	if v := os.Getenv(NNTPProxyAuthPass); v != "" {
		o.ProxyAuthPass = v
		keys = append(keys, KeyProxyAuthPass)
	}
	if v := os.Getenv(AdminUsernameEnv); v != "" {
		o.AdminUsername = v
		keys = append(keys, KeyAdminUsername)
	}
	o.Providers = readProvidersFromEnv()
	if len(o.Providers) > 0 {
		keys = append(keys, KeyProviders)
	}
	o.Indexers = readIndexersFromEnv()
	if len(o.Indexers) > 0 {
		keys = append(keys, KeyIndexers)
	}

	return o, keys
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
