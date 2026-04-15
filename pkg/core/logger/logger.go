package logger

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"streamnzb/pkg/core/env"
	"streamnzb/pkg/core/paths"
)

var Log *slog.Logger

const redactedValue = "[REDACTED]"
const CurrentLogFileName = "streamnzb.log"

var (
	sensitiveURLUserRe = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)([^/@\s]+)@`)
	sensitiveQueryRe   = regexp.MustCompile(`(?i)([?&](?:api[_-]?key|access[_-]?token|auth[_-]?token|refresh[_-]?token|token|password|passwd|pwd|secret|auth_session)=)([^&#\s;]+)`)
	sensitiveAssignRe  = regexp.MustCompile(`(?i)\b((?:api[_-]?key|access[_-]?token|auth[_-]?token|refresh[_-]?token|token|password|passwd|pwd|secret|auth_session)=)([^\s&#;]+)`)
	sensitiveJSONKVRe  = regexp.MustCompile(`(?i)("(?:api[_-]?key|access[_-]?token|auth[_-]?token|refresh[_-]?token|token|password|passwd|pwd|secret|auth_session)"\s*:\s*")([^"]+)`)
	authorizationKVRe  = regexp.MustCompile(`(?i)\b(authorization[=:]\s*)(bearer\s+|basic\s+)?([^\s,;]+)`)
	hexPathSegmentRe   = regexp.MustCompile(`(/)(?i:[0-9a-f]{64})(/|$|[?#])`)
	verboseNNTPLogging atomic.Bool
)

func sanitizeString(s string) string {
	if s == "" {
		return s
	}
	s = sensitiveURLUserRe.ReplaceAllString(s, `${1}`+redactedValue+`@`)
	s = sensitiveQueryRe.ReplaceAllString(s, `${1}`+redactedValue)
	s = sensitiveAssignRe.ReplaceAllString(s, `${1}`+redactedValue)
	s = sensitiveJSONKVRe.ReplaceAllString(s, `${1}`+redactedValue)
	s = authorizationKVRe.ReplaceAllString(s, `${1}${2}`+redactedValue)
	s = hexPathSegmentRe.ReplaceAllString(s, `${1}`+redactedValue+`${2}`)
	return s
}

func isSensitiveKey(key string) bool {
	if key == "" {
		return false
	}
	var b strings.Builder
	b.Grow(len(key))
	for _, r := range key {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	normalized := strings.ToLower(b.String())
	if normalized == "authorization" || normalized == "authsession" {
		return true
	}
	if normalized == "baseurl" || strings.HasSuffix(normalized, "baseurl") {
		return true
	}
	return strings.Contains(normalized, "apikey") ||
		strings.HasSuffix(normalized, "token") ||
		strings.Contains(normalized, "password") ||
		strings.Contains(normalized, "passwd") ||
		strings.Contains(normalized, "secret")
}

func sanitizeAttr(a slog.Attr) slog.Attr {
	if isSensitiveKey(a.Key) {
		return slog.String(a.Key, redactedValue)
	}
	switch a.Value.Kind() {
	case slog.KindString:
		return slog.String(a.Key, sanitizeString(a.Value.String()))
	case slog.KindAny:
		switch v := a.Value.Any().(type) {
		case string:
			return slog.String(a.Key, sanitizeString(v))
		case error:
			return slog.String(a.Key, sanitizeString(v.Error()))
		case fmt.Stringer:
			return slog.String(a.Key, sanitizeString(v.String()))
		default:
			return a
		}
	case slog.KindGroup:
		group := a.Value.Group()
		sanitized := make([]slog.Attr, 0, len(group))
		for _, child := range group {
			sanitized = append(sanitized, sanitizeAttr(child))
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(sanitized...)}
	default:
		return a
	}
}

func sanitizeAttrs(attrs []slog.Attr) []slog.Attr {
	if len(attrs) == 0 {
		return nil
	}
	sanitized := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		sanitized = append(sanitized, sanitizeAttr(a))
	}
	return sanitized
}

func sanitizeRecord(r slog.Record) slog.Record {
	sanitized := slog.NewRecord(r.Time, r.Level, sanitizeString(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		sanitized.AddAttrs(sanitizeAttr(a))
		return true
	})
	return sanitized
}

func formatLogMessage(r slog.Record, loc *time.Location) string {
	formattedTime := r.Time.In(loc)
	msg := fmt.Sprintf("time=%s level=%s msg=%q", formattedTime.Format("2006-01-02T15:04:05.000-07:00"), r.Level, r.Message)
	r.Attrs(func(a slog.Attr) bool {
		msg += fmt.Sprintf(" %s=%v", a.Key, a.Value)
		return true
	})
	return msg
}

type BroadcastHandler struct {
	slog.Handler
	ch chan<- string
}

func (h *BroadcastHandler) Handle(ctx context.Context, r slog.Record) error {
	r = sanitizeRecord(r)

	err := h.Handler.Handle(ctx, r)

	if h.ch != nil {

		locationMu.RLock()
		loc := logLocation
		locationMu.RUnlock()
		if loc == nil {
			loc = time.Local
		}

		msg := formatLogMessage(r, loc)

		select {
		case h.ch <- msg:
		default:

		}
	}
	return err
}

func (h *BroadcastHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &BroadcastHandler{Handler: h.Handler.WithAttrs(sanitizeAttrs(attrs)), ch: h.ch}
}

func (h *BroadcastHandler) WithGroup(name string) slog.Handler {
	return &BroadcastHandler{Handler: h.Handler.WithGroup(name), ch: h.ch}
}

var _ = time.Now

var broadcastCh chan<- string

func SetBroadcast(ch chan<- string) {
	broadcastCh = ch

}

func Init(levelStr string) {
	var level slog.Level
	switch strings.ToUpper(levelStr) {
	case "TRACE":
		level = slog.LevelDebug - 1
	case "DEBUG":
		level = slog.LevelDebug
	case "WARN":
		level = slog.LevelWarn
	case "ERROR":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	tzEnv := env.TZ()
	var loc *time.Location
	locationMu.Lock()
	if tzEnv != "" {
		loadedLoc, err := time.LoadLocation(tzEnv)
		if err != nil {

			loc = time.Local
			logLocation = time.Local
		} else {
			loc = loadedLoc
			logLocation = loadedLoc
		}
	} else {

		loc = time.Local
		logLocation = time.Local
	}
	locationMu.Unlock()

	dataDir := paths.GetDataDir()
	currentPath := GetCurrentLogPath()

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create log directory: %v\n", err)
	} else {
		logFileMu.Lock()
		if logFile == nil {
			if fi, err := os.Stat(currentPath); err == nil && fi.Mode().IsRegular() {
				ts := fi.ModTime().Format("20060102-150405")
				archivedName := fmt.Sprintf("streamnzb-%s.log", ts)
				archivedPath := filepath.Join(dataDir, archivedName)
				if renameErr := os.Rename(currentPath, archivedPath); renameErr != nil {
					fmt.Fprintf(os.Stderr, "Failed to rotate log file %s -> %s: %v\n", currentPath, archivedPath, renameErr)
				}
			}
			var err error
			logFile, err = os.OpenFile(currentPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to open log file %s: %v\n", currentPath, err)
			}
		}
		logFileMu.Unlock()
	}

	tzLoc := loc
	opts := &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {

			if a.Key == slog.TimeKey {

				t := a.Value.Time().In(tzLoc)
				return slog.String("time", t.Format("2006-01-02T15:04:05.000-07:00"))
			}
			return a
		},
	}

	baseHandler := slog.NewTextHandler(os.Stdout, opts)

	handler := &GlobalBroadcastHandler{
		Handler: baseHandler,
	}

	Log = slog.New(handler)
	slog.SetDefault(Log)

	locationMu.RLock()
	currentLoc := logLocation
	currentTZEnv := tzEnv
	locationMu.RUnlock()
	if currentLoc != nil {
		Log.Info("Logger initialized", "timezone", currentLoc.String(), "tz_env", currentTZEnv)
	}
}

type GlobalBroadcastHandler struct {
	slog.Handler
}

var (
	history     []string
	historyMu   sync.RWMutex
	maxHistory  = 500
	logFile     *os.File
	logFileMu   sync.Mutex
	logLocation *time.Location
	locationMu  sync.RWMutex
)

func (h *GlobalBroadcastHandler) Handle(ctx context.Context, r slog.Record) error {
	r = sanitizeRecord(r)

	locationMu.RLock()
	loc := logLocation
	locationMu.RUnlock()

	if loc == nil {
		loc = time.Local
	}

	msg := formatLogMessage(r, loc)

	historyMu.Lock()
	if len(history) >= maxHistory {
		history = history[1:]
	}
	history = append(history, msg)
	historyMu.Unlock()

	err := h.Handler.Handle(ctx, r)

	logFileMu.Lock()
	if logFile != nil {
		fmt.Fprintln(logFile, msg)
	}
	logFileMu.Unlock()

	if broadcastCh != nil {
		select {
		case broadcastCh <- msg:
		default:
		}
	}
	return err
}

func (h *GlobalBroadcastHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &GlobalBroadcastHandler{Handler: h.Handler.WithAttrs(sanitizeAttrs(attrs))}
}

func (h *GlobalBroadcastHandler) WithGroup(name string) slog.Handler {
	return &GlobalBroadcastHandler{Handler: h.Handler.WithGroup(name)}
}

func GetHistory() []string {
	historyMu.RLock()
	defer historyMu.RUnlock()

	cp := make([]string, len(history))
	copy(cp, history)
	return cp
}

func GetCurrentLogPath() string {
	return filepath.Join(paths.GetDataDir(), CurrentLogFileName)
}

// PurgeOldLogs removes older rotated log files so at most keepCount log files remain
// (the current streamnzb.log plus up to keepCount-1 archived streamnzb-*.log files).
// Only timestamped files (streamnzb-*.log) are considered for deletion; the current
// streamnzb.log is never removed. keepCount must be at least 1.
func PurgeOldLogs(keepCount int) {
	if keepCount < 1 {
		return
	}
	dataDir := paths.GetDataDir()
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return
	}
	type namedInfo struct {
		name string
		path string
		mod  time.Time
	}
	var archived []namedInfo
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if name == "streamnzb.log" || !strings.HasPrefix(name, "streamnzb-") || !strings.HasSuffix(name, ".log") {
			continue
		}
		path := filepath.Join(dataDir, name)
		info, err := e.Info()
		if err != nil {
			continue
		}
		archived = append(archived, namedInfo{name: name, path: path, mod: info.ModTime()})
	}
	maxArchived := keepCount - 1
	if len(archived) <= maxArchived {
		return
	}
	sort.Slice(archived, func(i, j int) bool { return archived[i].mod.Before(archived[j].mod) })
	for i := 0; i < len(archived)-maxArchived; i++ {
		_ = os.Remove(archived[i].path)
	}
}

func SetLevel(levelStr string) {
	Init(levelStr)
}

func SetVerboseNNTPLogging(enabled bool) {
	verboseNNTPLogging.Store(enabled)
}

func VerboseNNTPLoggingEnabled() bool {
	return verboseNNTPLogging.Load()
}

func VerboseNNTP(msg string, args ...any) {
	if !VerboseNNTPLoggingEnabled() {
		return
	}
	Debug(msg, args...)
}

func Trace(msg string, args ...any) {
	if Log == nil {
		return
	}
	Log.Log(context.TODO(), slog.LevelDebug-1, msg, args...)
}

func Debug(msg string, args ...any) {
	if Log == nil {
		return
	}
	Log.Debug(msg, args...)
}

func Info(msg string, args ...any) {
	if Log == nil {
		return
	}
	Log.Info(msg, args...)
}

func Warn(msg string, args ...any) {
	if Log == nil {
		return
	}
	Log.Warn(msg, args...)
}

func Error(msg string, args ...any) {
	if Log == nil {
		return
	}
	Log.Error(msg, args...)
}
