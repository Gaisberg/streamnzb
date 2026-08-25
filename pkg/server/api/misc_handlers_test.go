package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"testing"

	"streamnzb/pkg/core/logger"
	"streamnzb/pkg/core/paths"
)

// setTestDataDir points GetDataDir at a temp directory so tests don't
// touch the real AppData folder on Windows.
func setTestDataDir(t *testing.T) {
	t.Helper()
	tempDir := t.TempDir()
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
		t.Cleanup(func() { _ = os.Chdir(oldWD) })
	}
	if err := os.MkdirAll(paths.GetDataDir(), 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
}

func TestHandleDownloadLogsServesCurrentLogFile(t *testing.T) {
	setTestDataDir(t)

	const content = "time=2026-03-08T00:00:00.000+00:00 level=INFO msg=\"hello\"\n"
	if err := os.WriteFile(logger.GetCurrentLogPath(), []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	req := adminStreamRequest(http.MethodGet, "/api/logs/download", nil)
	rr := httptest.NewRecorder()

	adminServer().handleDownloadLogs(rr, req)

	res := rr.Result()
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", res.StatusCode, http.StatusOK)
	}
	if got := res.Header.Get("Content-Disposition"); got != `attachment; filename="streamnzb.log"` {
		t.Fatalf("Content-Disposition = %q", got)
	}
	if got := res.Header.Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(body) != content {
		t.Fatalf("body = %q, want %q", string(body), content)
	}
}

func TestHandleDownloadLogsReturnsNotFoundWhenMissing(t *testing.T) {
	setTestDataDir(t)

	req := adminStreamRequest(http.MethodGet, "/api/logs/download", nil)
	rr := httptest.NewRecorder()

	adminServer().handleDownloadLogs(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestHandleDownloadLogsRejectsNonGet(t *testing.T) {
	req := adminStreamRequest(http.MethodPost, "/api/logs/download", nil)
	rr := httptest.NewRecorder()

	adminServer().handleDownloadLogs(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusMethodNotAllowed)
	}
}

func TestClearStreamScopesNormalizesTickedStreams(t *testing.T) {
	for _, tc := range []struct {
		name  string
		input []string
		want  []string
	}{
		{"no selection means every stream", nil, nil},
		{"blanks are dropped", []string{"", "  "}, nil},
		{"all widens back to every stream", []string{"phone", "all"}, nil},
		{"duplicates collapse", []string{"phone", "phone", "tv"}, []string{"phone", "tv"}},
		{"values are trimmed", []string{" living-room "}, []string{"living-room"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := clearStreamScopes(tc.input)
			if len(got) != len(tc.want) {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %q, want %q", got, tc.want)
				}
			}
		})
	}
}
