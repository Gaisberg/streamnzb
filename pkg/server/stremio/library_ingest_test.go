package stremio

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"streamnzb/pkg/media/nzb"
	"streamnzb/pkg/search/parser"
)

func packNZB(t *testing.T, files ...string) *nzb.NZB {
	t.Helper()
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><nzb xmlns="http://www.newzbin.com/DTD/2003/nzb">`)
	for i, name := range files {
		fmt.Fprintf(&b, `<file poster="p" date="1" subject="[%d/%d] - &quot;%s&quot; yEnc (1/1)"><groups><group>alt.binaries.test</group></groups><segments><segment bytes="700000" number="1">%d@test</segment></segments></file>`, i+1, len(files), name, i)
	}
	b.WriteString(`</nzb>`)
	parsed, err := nzb.ParseWithContext(context.Background(), bytes.NewReader([]byte(b.String())))
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestContentSetsGroupEveryFileWithoutParsing(t *testing.T) {
	n := packNZB(t,
		"Show.Name.S01E03.1080p.WEB-GRP.mkv",
		"Show.Name.S01E01.1080p.WEB-GRP.mkv",
		"Show.Name.S01E01E02.1080p.WEB-GRP.mkv", // a double episode: its own set
		"Show.Name.S01E02.1080p.WEB-GRP.nfo",    // sidecar: not content
		"show.name.s01e01.sample.mkv",           // sample: not content
		"Weird_Name_Without_Numbers.mkv",        // unparseable: still a set
		"Show.Name.S01E04.1080p.WEB-GRP.rar",    // an archive in two volumes: one set
		"Show.Name.S01E04.1080p.WEB-GRP.r00",
		"Show.Name.S01E03.1080p.WEB-GRP.nfo", // sidecar sharing the first file's stem: not in its set
	)
	sets := n.ContentSets()
	var leads []string
	for _, set := range sets {
		leads = append(leads, set[0].Filename)
	}
	want := []string{
		"Show.Name.S01E03.1080p.WEB-GRP.mkv",
		"Show.Name.S01E01.1080p.WEB-GRP.mkv",
		"Show.Name.S01E01E02.1080p.WEB-GRP.mkv",
		"Weird_Name_Without_Numbers.mkv",
		"Show.Name.S01E04.1080p.WEB-GRP.rar",
	}
	if fmt.Sprint(leads) != fmt.Sprint(want) {
		t.Fatalf("content sets = %v, want %v", leads, want)
	}
	if len(sets[4]) != 2 {
		t.Fatalf("archive set should hold both volumes, got %d", len(sets[4]))
	}
	if len(sets[0]) != 1 {
		t.Fatalf("a sidecar must not join the media file's set, got %d files", len(sets[0]))
	}
	if files := n.GetSessionContentFilesForEpisode(parser.EpisodeTarget{File: "Show.Name.S01E03.1080p.WEB-GRP.mkv"}); len(files) != 1 {
		t.Fatalf("pinned set with a sidecar beside it = %d files", len(files))
	}

	// Pinning a file selects its whole set and ignores episode matching.
	target := parser.EpisodeTarget{File: "Show.Name.S01E04.1080p.WEB-GRP.r00"}
	if files := n.GetSessionContentFilesForEpisode(target); len(files) != 2 || files[0].Filename != "Show.Name.S01E04.1080p.WEB-GRP.rar" {
		t.Fatalf("pinned selection = %v", files)
	}
	if files := n.GetSessionContentFilesForEpisode(parser.EpisodeTarget{File: "Weird_Name_Without_Numbers.mkv"}); len(files) != 1 {
		t.Fatalf("pinned lone file = %v", files)
	}
}

func TestIngestContentTypeKnowsPacks(t *testing.T) {
	cases := []struct {
		name, requested string
		wantType        string
		wantS, wantE    int
	}{
		{"Show.Name.S01E02.1080p-GRP", "", "series", 1, 2},
		{"Show.Name.S01.1080p-GRP", "", "series", 1, 0},
		{"Show.Name.S01.1080p-GRP", "series", "series", 1, 0},
		{"Show.Name.COMPLETE.1080p-GRP", "series", "series", 0, 0},
		{"Movie.Name.2008.2160p-GRP", "", "movie", 0, 0},
		{"Movie.Name.2008.2160p-GRP", "series", "series", 0, 0},
		{"Show.Name.S01E02.1080p-GRP", "movie", "movie", 0, 0},
	}
	for _, c := range cases {
		gotType, s, e := ingestContentType(c.requested, parser.ParseReleaseTitle(c.name))
		if gotType != c.wantType || s != c.wantS || e != c.wantE {
			t.Errorf("%q/%q = %s s%de%d, want %s s%de%d", c.name, c.requested, gotType, s, e, c.wantType, c.wantS, c.wantE)
		}
	}
}

func TestLooksObfuscated(t *testing.T) {
	yes := []string{"449eee6adfa32ec60b6422215f8d35a5.mkv", "60jb556z863iqW2v2OsR.part001.rar", "3jB4qe6DgTVuUa4al.mkv"}
	no := []string{"House.of.the.Dragon.S03E01.1080p.WEB-DL-GRP.mkv", "Movie (2008).mkv", "abcdefghijklmnop.mkv", "Show_Name_S01E01.mkv"}
	for _, n := range yes {
		if !looksObfuscated(n) {
			t.Errorf("%q should look obfuscated", n)
		}
	}
	for _, n := range no {
		if looksObfuscated(n) {
			t.Errorf("%q should not look obfuscated", n)
		}
	}
}

func TestEpisodeMetaForSet(t *testing.T) {
	n := packNZB(t, "Show.Name.S01E03.1080p.WEB-GRP.mkv", "449eee6adfa32ec60b6422215f8d35a5.mkv", "Show.Name.E05.720p-GRP.mkv")
	infos := n.GetFileInfo()
	byName := map[string]*nzb.FileInfo{}
	for _, i := range infos {
		byName[i.Filename] = i
	}
	named, hashed, epOnly := byName["Show.Name.S01E03.1080p.WEB-GRP.mkv"], byName["449eee6adfa32ec60b6422215f8d35a5.mkv"], byName["Show.Name.E05.720p-GRP.mkv"]
	cases := []struct {
		name      string
		packS, pE int
		lead      *nzb.FileInfo
		multi     bool
		wantS, wE int
	}{
		{"pack: named file decides", 3, 2, named, true, 1, 3},
		{"pack: pack episode never applied to a hashed file", 3, 2, hashed, true, 3, 0},
		{"pack: episode-only name takes the pack season", 2, 0, epOnly, true, 2, 5},
		{"single: release name wins", 1, 7, hashed, false, 1, 7},
		{"single: file name when release names none", 1, 0, named, false, 1, 3},
		{"single: nothing parseable keeps season only", 4, 0, hashed, false, 4, 0},
	}
	for _, c := range cases {
		gs, ge := episodeMetaForSet("series", c.packS, c.pE, c.lead, c.multi)
		if gs != c.wantS || ge != c.wE {
			t.Errorf("%s: got s%de%d, want s%de%d", c.name, gs, ge, c.wantS, c.wE)
		}
	}
	if s, e := episodeMetaForSet("movie", 3, 2, named, true); s != 0 || e != 0 {
		t.Fatal("movies carry no episode metadata")
	}
}

func TestContainerTitleFallback(t *testing.T) {
	cases := map[string]string{
		`{"VideoCodec":"hevc","Title":"House.of.the.Dragon.S03E01.1080p.WEB.H264-FW"}`: "House.of.the.Dragon.S03E01.1080p.WEB.H264-FW",
		`{"Title":"House of the Dragon: S03E02 / Queen's Landing"}`:                    "House of the Dragon. S03E02 . Queen's Landing",
		`{"Title":"449eee6adfa32ec60b6422215f8d35a5"}`:                                 "",
		`{"Title":""}`:          "",
		`{"VideoCodec":"hevc"}`: "",
		``:                      "",
	}
	for in, want := range cases {
		if got := containerTitle(in); got != want {
			t.Errorf("containerTitle(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestIngestParallelism(t *testing.T) {
	// No pool: serial.
	if got := ingestParallelism(nil, 8); got != 1 {
		t.Fatalf("nil fetcher: %d", got)
	}
	if got := ingestParallelism(nil, 0); got != 1 {
		t.Fatalf("no sets still yields a worker: %d", got)
	}
}
