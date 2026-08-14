package stremio

import (
	"encoding/json"
	"strings"
	"testing"

	"streamnzb/pkg/auth"
)

func manifestName(t *testing.T, isAdmin bool, streamName, addonName string) string {
	t.Helper()
	data, err := NewManifest("1.2.3").ToJSONForDevice(isAdmin, streamName, addonName)
	if err != nil {
		t.Fatalf("ToJSONForDevice failed: %v", err)
	}
	var out Manifest
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("manifest is not valid JSON: %v", err)
	}
	return out.Name
}

func TestManifestNameDefaultsToServicePlusStream(t *testing.T) {
	if got := manifestName(t, false, "Living Room", ""); got != "StreamNZB · Living Room" {
		t.Fatalf("name = %q, want the service name with the stream appended", got)
	}
	if got := manifestName(t, false, "", ""); got != "StreamNZB" {
		t.Fatalf("name = %q, want the bare service name when there is no stream", got)
	}
}

func TestManifestNameOverrideReplacesTheWholeLabel(t *testing.T) {
	got := manifestName(t, false, "Living Room", "Usenet 4K")
	if got != "Usenet 4K" {
		t.Fatalf("name = %q, want the override alone — a stream setting one is not asking for a suffix", got)
	}
}

func TestManifestNameOverrideIsTrimmedAndCapped(t *testing.T) {
	if got := manifestName(t, false, "Living Room", "   "); got != "StreamNZB · Living Room" {
		t.Fatalf("name = %q, want the default back when the override is only whitespace", got)
	}

	long := strings.Repeat("a", MaxAddonNameLength+25)
	got := manifestName(t, false, "Living Room", long)
	if len(got) != MaxAddonNameLength {
		t.Fatalf("override length = %d, want it capped at %d", len(got), MaxAddonNameLength)
	}
}

func TestNormalizeAddonName(t *testing.T) {
	if got := NormalizeAddonName("  Kids  "); got != "Kids" {
		t.Fatalf("NormalizeAddonName = %q, want %q", got, "Kids")
	}
	if got := NormalizeAddonName(""); got != "" {
		t.Fatalf("NormalizeAddonName = %q, want empty", got)
	}
}

func TestServiceNameFallsBackToTheDefault(t *testing.T) {
	if got := ServiceName(nil); got != DefaultServiceName {
		t.Fatalf("ServiceName(nil) = %q, want %q", got, DefaultServiceName)
	}
	if got := ServiceName(&auth.Stream{Username: "Living Room"}); got != DefaultServiceName {
		t.Fatalf("ServiceName without an override = %q, want %q", got, DefaultServiceName)
	}
	if got := ServiceName(&auth.Stream{Username: "Living Room", AddonName: "  Usenet 4K  "}); got != "Usenet 4K" {
		t.Fatalf("ServiceName = %q, want the trimmed override", got)
	}
}

func TestStreamDisplayNameUsesOverrideWithoutTheStreamSuffix(t *testing.T) {
	// Without an override the label is two lines so several installed configs
	// are tellable apart.
	if got := streamDisplayName(&auth.Stream{Username: "Living Room"}); got != "StreamNZB\nLiving Room" {
		t.Fatalf("display name = %q, want the default two-line label", got)
	}
	// With one, the override IS the name — appending the stream name under it
	// would undo the point of setting it.
	if got := streamDisplayName(&auth.Stream{Username: "Living Room", AddonName: "Dandelion is 1337"}); got != "Dandelion is 1337" {
		t.Fatalf("display name = %q, want the override alone", got)
	}
	if got := streamDisplayName(nil); got != DefaultServiceName {
		t.Fatalf("display name for no stream = %q, want %q", got, DefaultServiceName)
	}
}
