package logger

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"streamnzb/pkg/core/paths"
)

func TestSanitizeStringRedactsSensitiveData(t *testing.T) {
	token := strings.Repeat("a", 64)
	input := `https://user:pass@example.com/` + token + `/play/1?api_key=secret&foo=ok Authorization=Bearer topsecret auth_session=session123 password=hunter2`
	got := sanitizeString(input)
	for _, secret := range []string{"user:pass", token, "secret", "topsecret", "session123", "hunter2"} {
		if strings.Contains(got, secret) {
			t.Fatalf("sanitizeString leaked %q in %q", secret, got)
		}
	}
	if !strings.Contains(got, redactedValue) {
		t.Fatalf("sanitizeString did not redact anything: %q", got)
	}
}

func TestSanitizeAttrRedactsSensitiveKeysAndStringValues(t *testing.T) {
	if got := sanitizeAttr(slog.String("api_key", "secret")).Value.String(); got != redactedValue {
		t.Fatalf("api_key = %q, want %q", got, redactedValue)
	}
	if got := sanitizeAttr(slog.String("base_url", "https://example.com/addon")).Value.String(); got != redactedValue {
		t.Fatalf("base_url = %q, want %q", got, redactedValue)
	}
	urlAttr := sanitizeAttr(slog.String("url", "https://example.com/search?token=secret"))
	if strings.Contains(urlAttr.Value.String(), "secret") {
		t.Fatalf("url attr leaked secret: %q", urlAttr.Value.String())
	}
	group := sanitizeAttr(slog.Group("headers", slog.String("Authorization", "Bearer secret")))
	if got := group.Value.Group()[0].Value.String(); got != redactedValue {
		t.Fatalf("group authorization = %q, want %q", got, redactedValue)
	}
}

func TestGlobalBroadcastHandlerRedactsUnderlyingOutputAndHistory(t *testing.T) {
	historyMu.Lock()
	history = nil
	historyMu.Unlock()
	broadcastCh = nil
	logFileMu.Lock()
	logFile = nil
	logFileMu.Unlock()
	locationMu.Lock()
	logLocation = time.UTC
	locationMu.Unlock()

	var buf bytes.Buffer
	h := &GlobalBroadcastHandler{Handler: slog.NewTextHandler(&buf, nil)}
	r := slog.NewRecord(time.Unix(0, 0), slog.LevelInfo, "fetch https://example.com?api_key=secret", 0)
	r.AddAttrs(
		slog.String("Authorization", "Bearer supersecret"),
		slog.String("url", "https://user:pass@example.com/"+strings.Repeat("b", 64)+"/play/1?token=abc"),
	)
	if err := h.Handle(context.Background(), r); err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	output := buf.String()
	historyEntries := GetHistory()
	if len(historyEntries) != 1 {
		t.Fatalf("expected 1 history entry, got %d", len(historyEntries))
	}
	for _, text := range []string{output, historyEntries[0]} {
		for _, secret := range []string{"secret", "supersecret", "user:pass", strings.Repeat("b", 64), "token=abc"} {
			if strings.Contains(text, secret) {
				t.Fatalf("log output leaked %q in %q", secret, text)
			}
		}
		if !strings.Contains(text, redactedValue) {
			t.Fatalf("expected redacted output, got %q", text)
		}
	}
}

func TestInitTwiceKeepsCurrentLogFile(t *testing.T) {
	tempDir := t.TempDir()

	// On Windows, GetDataDir uses LOCALAPPDATA; point it at tempDir so
	// logs land there instead of the real AppData folder.
	if runtime.GOOS == "windows" {
		t.Setenv("LOCALAPPDATA", tempDir)
	} else {
		oldWD, err := os.Getwd()
		if err != nil {
			t.Fatalf("Getwd: %v", err)
		}
		if err := os.Chdir(tempDir); err != nil {
			t.Fatalf("Chdir temp dir: %v", err)
		}
		defer func() { _ = os.Chdir(oldWD) }()
	}

	dataDir := paths.GetDataDir()
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	logFileMu.Lock()
	oldLogFile := logFile
	if oldLogFile != nil {
		_ = oldLogFile.Close()
	}
	logFile = nil
	logFileMu.Unlock()
	defer func() {
		logFileMu.Lock()
		if logFile != nil {
			_ = logFile.Close()
		}
		logFile = nil
		logFileMu.Unlock()
	}()

	if err := os.WriteFile(GetCurrentLogPath(), []byte("old log\n"), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	Init("INFO")
	archived, err := filepath.Glob(filepath.Join(dataDir, "streamnzb-*.log"))
	if err != nil {
		t.Fatalf("Glob after first init: %v", err)
	}
	if len(archived) != 1 {
		t.Fatalf("expected 1 archived log after first init, got %d", len(archived))
	}

	SetLevel("DEBUG")
	archived, err = filepath.Glob(filepath.Join(dataDir, "streamnzb-*.log"))
	if err != nil {
		t.Fatalf("Glob after second init: %v", err)
	}
	if len(archived) != 1 {
		t.Fatalf("expected second init to keep current log file, got %d archived logs", len(archived))
	}

	Info("still writing to current log")
	content, err := os.ReadFile(GetCurrentLogPath())
	if err != nil {
		t.Fatalf("ReadFile current log: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "still writing to current log") {
		t.Fatalf("expected current log file to contain new log entry, got %q", text)
	}
	if !strings.Contains(text, "Logger initialized") {
		t.Fatalf("expected current log file to remain active after second init, got %q", text)
	}
}

func TestVerboseNNTPLoggingToggle(t *testing.T) {
	previous := VerboseNNTPLoggingEnabled()
	t.Cleanup(func() {
		SetVerboseNNTPLogging(previous)
	})

	SetVerboseNNTPLogging(false)
	if VerboseNNTPLoggingEnabled() {
		t.Fatal("expected verbose NNTP logging to be disabled")
	}

	SetVerboseNNTPLogging(true)
	if !VerboseNNTPLoggingEnabled() {
		t.Fatal("expected verbose NNTP logging to be enabled")
	}

	SetVerboseNNTPLogging(false)
	if VerboseNNTPLoggingEnabled() {
		t.Fatal("expected verbose NNTP logging to be disabled again")
	}
}
