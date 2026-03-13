package env

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	ADDONPort             = "ADDON_PORT"
	ADDONBaseURL          = "ADDON_BASE_URL"
	LOGLevel              = "LOG_LEVEL"
	KeepLogFilesEnv       = "KEEP_LOG_FILES"
	AvailNZBURLEnv        = "AVAILNZB_URL"
	AvailNZBAPIKeyEnv     = "AVAILNZB_API_KEY"
	TMDBAPIKeyEnv         = "TMDB_API_KEY"
	TVDBAPIKeyEnv         = "TVDB_API_KEY"
	NNTPProxyPort         = "NNTP_PROXY_PORT"
	NNTPProxyHost         = "NNTP_PROXY_HOST"
	NNTPProxyAuthUser     = "NNTP_PROXY_AUTH_USER"
	NNTPProxyAuthPass     = "NNTP_PROXY_AUTH_PASS"
	MemoryLimitMBEnv      = "MEMORY_LIMIT_MB"
	AvailNZBModeEnv       = "AVAILNZB_MODE"
	TZVar                 = "TZ"
	ProviderPrefix        = "PROVIDER_"
	IndexerPrefix         = "INDEXER_"
	IndexerQueryHeaderEnv = "INDEXER_QUERY_HEADER"
	IndexerGrabHeaderEnv  = "INDEXER_GRAB_HEADER"
)

var DefaultIndexerUserAgent = "StreamNZB/dev"

func TZ() string {
	return os.Getenv(TZVar)
}

func IndexerQueryHeader() string {
	if v := os.Getenv(IndexerQueryHeaderEnv); v != "" {
		return v
	}
	return DefaultIndexerUserAgent
}

func IndexerGrabHeader() string {
	if v := os.Getenv(IndexerGrabHeaderEnv); v != "" {
		return v
	}
	return DefaultIndexerUserAgent
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
	APIPath string
	Type    string
	Enabled *bool
}

// ConfigValues holds all user-facing configuration read from environment variables.
type ConfigValues struct {
	AddonPort     int
	AddonBaseURL  string
	LogLevel      string
	KeepLogFiles  int
	AvailNZBURL   string
	AvailNZBAPIKey string
	TMDBAPIKey    string
	TVDBAPIKey    string
	ProxyPort     int
	ProxyHost     string
	ProxyAuthUser string
	ProxyAuthPass string
	MemoryLimitMB int
	AvailNZBMode  string
	Providers     []Provider
	Indexers      []Indexer
}

// ReadConfig reads all configuration from environment variables, applying defaults.
func ReadConfig() ConfigValues {
	return ConfigValues{
		AddonPort:      getEnvInt(ADDONPort, 7000),
		AddonBaseURL:   getEnv(ADDONBaseURL, "http://localhost:7000"),
		LogLevel:       getEnv(LOGLevel, "INFO"),
		KeepLogFiles:   max(getEnvInt(KeepLogFilesEnv, 9), 1),
		AvailNZBURL:    getEnv(AvailNZBURLEnv, "https://snzb.stream"),
		AvailNZBAPIKey: os.Getenv(AvailNZBAPIKeyEnv),
		TMDBAPIKey:     os.Getenv(TMDBAPIKeyEnv),
		TVDBAPIKey:     os.Getenv(TVDBAPIKeyEnv),
		ProxyPort:      getEnvInt(NNTPProxyPort, 119),
		ProxyHost:      getEnv(NNTPProxyHost, "0.0.0.0"),
		ProxyAuthUser:  os.Getenv(NNTPProxyAuthUser),
		ProxyAuthPass:  os.Getenv(NNTPProxyAuthPass),
		MemoryLimitMB:  getEnvInt(MemoryLimitMBEnv, 512),
		AvailNZBMode:   os.Getenv(AvailNZBModeEnv),
		Providers:      readProvidersFromEnv(),
		Indexers:       readIndexersFromEnv(),
	}
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
			APIPath: getEnv(prefix+"API_PATH", "/api"),
			Type:    getEnv(prefix+"TYPE", "newznab"),
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
