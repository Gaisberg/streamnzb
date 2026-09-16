package unpack

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"streamnzb/pkg/media/rar"
)

// A characterization test over real archives, for changing the RAR reader
// without changing what it reads.
//
// The reader's failure mode is not a crash. A wrong part offset or packed size
// hands the player bytes from the wrong place in the archive, and the release
// plays as corrupt — which looks exactly like a damaged post. Unit tests over
// hand-built structures cannot catch that, because the thing at risk is the
// agreement between this code and what real packers actually emit.
//
// So: record what the current reader says about a set of real archives, change
// the reader, and diff. The corpus is whatever archives the person running it
// has, because a corpus that ships with the repo can only ever contain the
// cases somebody already thought of.
//
//	STREAMNZB_RAR_CORPUS=/path/to/archives go test ./pkg/media/unpack/ -run TestRARCorpus
//
// The first run writes the golden and passes. Keep the golden from before a
// change, run again after, and any difference is a regression to explain.
// Re-record deliberately with STREAMNZB_RAR_CORPUS_UPDATE=1.
//
// The golden lives in the corpus directory rather than in the repository: it
// is keyed to one person's archives, names their files, and is a tool for a
// migration rather than a fixture the project carries.
const (
	corpusEnv         = "STREAMNZB_RAR_CORPUS"
	corpusPasswordEnv = "STREAMNZB_RAR_CORPUS_PASSWORD"
	corpusUpdateEnv   = "STREAMNZB_RAR_CORPUS_UPDATE"
	corpusGoldenName  = "rar-golden.json"
)

// corpusPart is FilePartInfo with the fields that cannot be compared removed.
// The key material is derived per archive and has no business being written to
// disk; its presence and length are what a reader change could get wrong.
type corpusPart struct {
	Path              string `json:"path"`
	DataOffset        int64  `json:"dataOffset"`
	PackedSize        int64  `json:"packedSize"`
	UnpackedSize      int64  `json:"unpackedSize"`
	Stored            bool   `json:"stored"`
	Compressed        bool   `json:"compressed"`
	CompressionMethod string `json:"compressionMethod"`
	Encrypted         bool   `json:"encrypted"`
	SaltLen           int    `json:"saltLen,omitempty"`
	AesKeyLen         int    `json:"aesKeyLen,omitempty"`
	AesIVLen          int    `json:"aesIVLen,omitempty"`
	KdfIterations     int    `json:"kdfIterations,omitempty"`
}

type corpusFile struct {
	Name              string       `json:"name"`
	TotalPackedSize   int64        `json:"totalPackedSize"`
	TotalUnpackedSize int64        `json:"totalUnpackedSize"`
	AnyEncrypted      bool         `json:"anyEncrypted"`
	AllStored         bool         `json:"allStored"`
	Compressed        bool         `json:"compressed"`
	CompressionMethod string       `json:"compressionMethod"`
	Parts             []corpusPart `json:"parts"`
}

// corpusEntry is one archive set's reading. Err is recorded rather than
// failing the test: an archive this reader cannot open today is a fact about
// it, and a change that makes it openable — or breaks a different one — is
// exactly what the diff should show.
type corpusEntry struct {
	Files []corpusFile `json:"files,omitempty"`
	Err   string       `json:"err,omitempty"`
}

func TestRARCorpus(t *testing.T) {
	root := os.Getenv(corpusEnv)
	if root == "" {
		t.Skipf("set %s to a directory of RAR archives to run the corpus check", corpusEnv)
	}
	firstVolumes, err := findFirstVolumes(root)
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	if len(firstVolumes) == 0 {
		t.Fatalf("no first volumes found under %s", root)
	}
	t.Logf("corpus: %d archive set(s) under %s", len(firstVolumes), root)

	got := make(map[string]corpusEntry, len(firstVolumes))
	for _, vol := range firstVolumes {
		key, err := filepath.Rel(root, vol)
		if err != nil {
			key = vol
		}
		got[filepath.ToSlash(key)] = readArchiveSet(root, vol, os.Getenv(corpusPasswordEnv))
	}

	assertCompressedStillReadsItsHeader(t, firstVolumes, os.Getenv(corpusPasswordEnv))

	goldenPath := filepath.Join(root, corpusGoldenName)
	if os.Getenv(corpusUpdateEnv) != "" {
		writeGolden(t, goldenPath, got)
		t.Logf("wrote %s (%d entries)", goldenPath, len(got))
		return
	}
	blob, err := os.ReadFile(goldenPath)
	if os.IsNotExist(err) {
		writeGolden(t, goldenPath, got)
		t.Logf("no golden yet — wrote %s (%d entries). Re-run after changing the reader.", goldenPath, len(got))
		return
	}
	if err != nil {
		t.Fatalf("reading golden: %v", err)
	}
	var want map[string]corpusEntry
	if err := json.Unmarshal(blob, &want); err != nil {
		t.Fatalf("parsing golden: %v", err)
	}
	compareCorpus(t, want, got)
}

// readArchiveSet lists one archive set with the same options the scanner uses,
// so the fingerprint reflects how the product reads an archive rather than how
// the library can be made to read one.
func readArchiveSet(root, firstVolume, password string) corpusEntry {
	opts := []rar.Option{
		rar.ParallelRead(false),
		rar.SkipVolumeCheck,
		rar.ListTolerant,
		rar.ListFromAnyVolume,
	}
	if password != "" {
		opts = append(opts, rar.Password(password))
	}
	infos, err := rar.ListArchiveInfo(firstVolume, opts...)
	if err != nil {
		return corpusEntry{Err: err.Error()}
	}
	files := make([]corpusFile, 0, len(infos))
	for _, info := range infos {
		files = append(files, normalizeArchiveFile(root, info))
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return corpusEntry{Files: files}
}

func normalizeArchiveFile(root string, info rar.ArchiveFileInfo) corpusFile {
	out := corpusFile{
		Name:              info.Name,
		TotalPackedSize:   info.TotalPackedSize,
		TotalUnpackedSize: info.TotalUnpackedSize,
		AnyEncrypted:      info.AnyEncrypted,
		AllStored:         info.AllStored,
		Compressed:        info.Compressed,
		CompressionMethod: info.CompressionMethod,
	}
	for _, p := range info.Parts {
		path := p.Path
		if rel, err := filepath.Rel(root, p.Path); err == nil {
			path = filepath.ToSlash(rel)
		}
		out.Parts = append(out.Parts, corpusPart{
			Path:              path,
			DataOffset:        p.DataOffset,
			PackedSize:        p.PackedSize,
			UnpackedSize:      p.UnpackedSize,
			Stored:            p.Stored,
			Compressed:        p.Compressed,
			CompressionMethod: p.CompressionMethod,
			Encrypted:         p.Encrypted,
			SaltLen:           len(p.Salt),
			AesKeyLen:         len(p.AesKey),
			AesIVLen:          len(p.AesIV),
			KdfIterations:     p.KdfIterations,
		})
	}
	return out
}

// findFirstVolumes collects the archive sets under root, one entry per set.
// The same predicate the scanner uses decides what starts a set, so the corpus
// covers the naming schemes the product actually recognises.
func findFirstVolumes(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(path), ExtRar) {
			return nil
		}
		if !isFirstVolumeName(path) {
			return nil
		}
		out = append(out, path)
		return nil
	})
	sort.Strings(out)
	return out, err
}

func writeGolden(t *testing.T, path string, entries map[string]corpusEntry) {
	t.Helper()
	blob, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		t.Fatalf("encoding golden: %v", err)
	}
	if err := os.WriteFile(path, append(blob, '\n'), 0o644); err != nil {
		t.Fatalf("writing golden: %v", err)
	}
}

// compareCorpus reports differences per archive set and per file, because "the
// golden does not match" over a 2 GB set is not a usable failure message.
func compareCorpus(t *testing.T, want, got map[string]corpusEntry) {
	t.Helper()
	for key, wantEntry := range want {
		gotEntry, ok := got[key]
		if !ok {
			t.Errorf("%s: in the golden but not in the corpus now", key)
			continue
		}
		if wantEntry.Err != gotEntry.Err {
			t.Errorf("%s: error changed\n  was: %q\n  now: %q", key, wantEntry.Err, gotEntry.Err)
			continue
		}
		compareFiles(t, key, wantEntry.Files, gotEntry.Files)
	}
	for key := range got {
		if _, ok := want[key]; !ok {
			t.Errorf("%s: present now but not in the golden — re-record if this set is new", key)
		}
	}
}

func compareFiles(t *testing.T, key string, want, got []corpusFile) {
	t.Helper()
	if len(want) != len(got) {
		t.Errorf("%s: %d file(s) in the golden, %d now", key, len(want), len(got))
		return
	}
	for i := range want {
		w, g := want[i], got[i]
		if w.Name != g.Name {
			t.Errorf("%s: file %d is %q, was %q", key, i, g.Name, w.Name)
			continue
		}
		if diff := describeFileDiff(w, g); diff != "" {
			t.Errorf("%s: %s: %s", key, w.Name, diff)
		}
	}
}

func describeFileDiff(want, got corpusFile) string {
	var diffs []string
	add := func(field string, w, g any) {
		if fmt.Sprint(w) != fmt.Sprint(g) {
			diffs = append(diffs, fmt.Sprintf("%s was %v, now %v", field, w, g))
		}
	}
	add("totalPackedSize", want.TotalPackedSize, got.TotalPackedSize)
	add("totalUnpackedSize", want.TotalUnpackedSize, got.TotalUnpackedSize)
	add("allStored", want.AllStored, got.AllStored)
	add("compressed", want.Compressed, got.Compressed)
	add("compressionMethod", want.CompressionMethod, got.CompressionMethod)
	add("anyEncrypted", want.AnyEncrypted, got.AnyEncrypted)
	add("part count", len(want.Parts), len(got.Parts))
	if len(want.Parts) == len(got.Parts) {
		for i := range want.Parts {
			w, g := want.Parts[i], got.Parts[i]
			// The offsets are the whole point: a wrong one plays the wrong
			// bytes without any error being raised anywhere.
			add(fmt.Sprintf("part[%d].path", i), w.Path, g.Path)
			add(fmt.Sprintf("part[%d].dataOffset", i), w.DataOffset, g.DataOffset)
			add(fmt.Sprintf("part[%d].packedSize", i), w.PackedSize, g.PackedSize)
			add(fmt.Sprintf("part[%d].unpackedSize", i), w.UnpackedSize, g.UnpackedSize)
			add(fmt.Sprintf("part[%d].stored", i), w.Stored, g.Stored)
			add(fmt.Sprintf("part[%d].encrypted", i), w.Encrypted, g.Encrypted)
			add(fmt.Sprintf("part[%d].saltLen", i), w.SaltLen, g.SaltLen)
			add(fmt.Sprintf("part[%d].aesKeyLen", i), w.AesKeyLen, g.AesKeyLen)
		}
	}
	return strings.Join(diffs, "; ")
}

// assertCompressedStillReadsItsHeader pins the contract the reader keeps with
// compressed archives now that it cannot decompress them.
//
// A compressed release is healthy — its articles are fine, it simply cannot be
// streamed, because streaming maps a byte range onto packed bytes. Callers
// tell that apart from an unreadable archive by reading the header and finding
// Stored false, and they report it as unstreamable rather than reporting the
// release bad to a community database. So refusing at Next() rather than at
// Read() would turn "this release cannot be streamed" into "this release is
// broken", which is a wrong answer sent to other people.
func assertCompressedStillReadsItsHeader(t *testing.T, firstVolumes []string, password string) {
	t.Helper()
	checked := 0
	for _, vol := range firstVolumes {
		f, err := os.Open(vol)
		if err != nil {
			continue
		}
		var opts []rar.Option
		if password != "" {
			opts = append(opts, rar.Password(password))
		}
		r, err := rar.NewReader(f, opts...)
		if err != nil {
			f.Close()
			continue
		}
		hdr, err := r.Next()
		if err != nil || hdr == nil || hdr.Stored {
			f.Close()
			continue // not a compressed set, or not openable on its own
		}
		checked++
		if _, err := r.Read(make([]byte, 64)); err == nil {
			t.Errorf("%s: reading compressed data should fail, it succeeded", filepath.Base(vol))
		}
		f.Close()
	}
	if checked == 0 {
		t.Log("no compressed archive in the corpus — the refusal path went unexercised")
		return
	}
	t.Logf("compressed archives still reporting their headers: %d", checked)
}
