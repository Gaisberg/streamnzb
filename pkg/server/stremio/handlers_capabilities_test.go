package stremio

import (
	"testing"

	"streamnzb/pkg/core/persistence"
	"streamnzb/pkg/release"
)

func libRelease(capsJSON string) *release.Release {
	return &release.Release{
		Title:         "House of the Dragon S01E03 2160p DV",
		IsLibrary:     true,
		SourceIndexer: &persistence.LibraryItem{MediaCapsJSON: capsJSON},
	}
}

func TestCapsSummaryLineLibraryOnly(t *testing.T) {
	rel := libRelease(`{"VideoCodec":"hevc","Profile":"Main 10","Height":2160,"BitDepth":10,"HDR":"HDR10","AudioCodec":"eac3"}`)
	got := capsSummaryLine(rel)
	if got == "" {
		t.Fatal("expected a caps summary for a library release with caps")
	}
	// Fresh (non-library) release must never surface caps (they aren't known).
	fresh := &release.Release{Title: "x", IsLibrary: false}
	if capsSummaryLine(fresh) != "" {
		t.Error("non-library release must not surface caps")
	}
	// Library release with no persisted caps yields nothing.
	noCaps := &release.Release{Title: "y", IsLibrary: true, SourceIndexer: &persistence.LibraryItem{}}
	if capsSummaryLine(noCaps) != "" {
		t.Error("library release without caps must not surface caps")
	}
}
