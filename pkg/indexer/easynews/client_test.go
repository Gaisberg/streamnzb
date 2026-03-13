package easynews

import (
	"io"
	"log/slog"
	"testing"

	"streamnzb/pkg/core/logger"
)

func init() {
	logger.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNewClientRequiresCredentials(t *testing.T) {
	_, err := NewClient("", "", "test", "", 0, 0)
	if err == nil {
		t.Fatal("expected error for empty credentials")
	}

	c, err := NewClient("user", "pass", "test", "", 10, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Name() != "test" {
		t.Fatalf("Name() = %q, want %q", c.Name(), "test")
	}
	usage := c.GetUsage()
	if usage.APIHitsLimit != 10 || usage.DownloadsLimit != 5 {
		t.Fatalf("unexpected limits: api=%d downloads=%d", usage.APIHitsLimit, usage.DownloadsLimit)
	}
}
