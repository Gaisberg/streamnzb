package config

import (
	"encoding/json"
	"testing"
)

// A provider that pinned a depth must survive the config round trip, and one
// that did not must not gain a pinned value: an omitted field is what keeps it
// inheriting the deployment default.
func TestProviderPipelineDepthRoundTrip(t *testing.T) {
	depth := 5
	cfg := Config{Providers: []Provider{
		{Name: "near", Host: "a", PipelineDepth: &depth},
		{Name: "far", Host: "b"},
	}}

	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var back Config
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Providers[0].PipelineDepth == nil || *back.Providers[0].PipelineDepth != 5 {
		t.Fatalf("pinned depth = %v, want 5", back.Providers[0].PipelineDepth)
	}
	if back.Providers[1].PipelineDepth != nil {
		t.Fatalf("unset provider gained depth %v; it must stay on the default", *back.Providers[1].PipelineDepth)
	}

	var asMap map[string]any
	if err := json.Unmarshal(raw, &asMap); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	providers := asMap["providers"].([]any)
	if _, present := providers[1].(map[string]any)["pipeline_depth"]; present {
		t.Fatal("an unset depth was serialized; the UI would read it back as pinned")
	}
}
