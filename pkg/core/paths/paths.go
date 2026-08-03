package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func GetDataDir() string {
	if envPath := strings.TrimSpace(os.Getenv("CONFIG_PATH")); envPath != "" {
		clean := filepath.Clean(envPath)
		if fi, err := os.Stat(clean); err == nil && fi.IsDir() {
			return clean
		}
		return filepath.Dir(clean)
	}
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return "/app/data"
	}
	if _, err := os.Stat("config.json"); err == nil {
		return "."
	}
	if runtime.GOOS == "windows" {
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			return filepath.Join(local, "streamnzb")
		}
	}
	return "."
}
