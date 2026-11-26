package persistence

import (
	"errors"
	"testing"
)

func TestExportStoreTreeOperations(t *testing.T) {
	es := openTestManager(t).ExportStore()
	if err := es.EnsureRoots(); err != nil {
		t.Fatal(err)
	}
	if err := es.EnsureRoots(); err != nil {
		t.Fatal("EnsureRoots must be idempotent:", err)
	}
	nodes, err := es.Nodes()
	if err != nil || len(nodes) != 1 {
		t.Fatalf("roots: %v %v", nodes, err)
	}
	downloads, ok, _ := es.Child(0, ExportRootDownloads)
	if !ok || !downloads.IsDir() {
		t.Fatal("downloads root missing")
	}
	// The *arr makes its own root folder at the top.
	media, err := es.Mkdir(0, "media")
	if err != nil {
		t.Fatal(err)
	}

	job, err := es.Mkdir(downloads.ID, "Iron.Man.2008-GRP")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := es.Mkdir(downloads.ID, "Iron.Man.2008-GRP"); !errors.Is(err, ErrExportExists) {
		t.Fatalf("duplicate mkdir: %v", err)
	}
	if _, err := es.Mkdir(downloads.ID, "a/b"); !errors.Is(err, ErrExportBadName) {
		t.Fatalf("bad name: %v", err)
	}
	file, err := es.AddFile(job.ID, "Iron.Man.2008-GRP.mkv", "movie:tt1:Iron.Man.2008-GRP")
	if err != nil {
		t.Fatal(err)
	}
	// Republishing replaces the alias rather than failing.
	again, err := es.AddFile(job.ID, "Iron.Man.2008-GRP.mkv", "movie:tt1:Iron.Man.2008-GRP.v2")
	if err != nil || again.ID == file.ID || again.ItemID != "movie:tt1:Iron.Man.2008-GRP.v2" {
		t.Fatalf("replace alias: %+v %v", again, err)
	}

	// The *arr imports: makes its folder and renames the file into it.
	movieDir, err := es.Mkdir(media.ID, "Iron Man (2008)")
	if err != nil {
		t.Fatal(err)
	}
	if err := es.Rename(again.ID, movieDir.ID, "Iron Man (2008) Bluray-2160p.mkv"); err != nil {
		t.Fatal(err)
	}
	moved, ok, _ := es.Child(movieDir.ID, "Iron Man (2008) Bluray-2160p.mkv")
	if !ok || moved.ID != again.ID || moved.ItemID != "movie:tt1:Iron.Man.2008-GRP.v2" {
		t.Fatalf("after rename: %+v ok=%v", moved, ok)
	}
	if _, stillThere, _ := es.Child(job.ID, "Iron.Man.2008-GRP.mkv"); stillThere {
		t.Fatal("source name should be gone after rename")
	}
	// Then it tidies the download folder.
	if err := es.Remove(job.ID); err != nil {
		t.Fatal(err)
	}
	// Guards.
	if err := es.Remove(movieDir.ID); !errors.Is(err, ErrExportNotEmpty) {
		t.Fatalf("remove non-empty dir: %v", err)
	}
	if err := es.Remove(downloads.ID); !errors.Is(err, ErrExportRoot) {
		t.Fatalf("remove downloads: %v", err)
	}
	if err := es.Rename(downloads.ID, media.ID, "x"); !errors.Is(err, ErrExportRoot) {
		t.Fatalf("rename downloads: %v", err)
	}
	if err := es.Rename(media.ID, 0, ExportRootDownloads); !errors.Is(err, ErrExportRoot) {
		t.Fatalf("replaced downloads: %v", err)
	}
	// A client-made top-level folder can be renamed at the top, but not to
	// the reserved name of the generated folder.
	if err := es.Rename(media.ID, 0, ExportReservedLibrary); !errors.Is(err, ErrExportExists) {
		t.Fatalf("rename onto reserved name: %v", err)
	}
	if _, err := es.Mkdir(0, ExportReservedLibrary); !errors.Is(err, ErrExportExists) {
		t.Fatalf("mkdir of reserved name: %v", err)
	}
	if err := es.Rename(media.ID, 0, "collection"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := es.Child(0, "collection"); !ok {
		t.Fatal("renamed top-level folder missing")
	}
	sub, _ := es.Mkdir(movieDir.ID, "sub")
	if err := es.Rename(movieDir.ID, sub.ID, "loop"); err == nil {
		t.Fatal("moving a directory into itself must fail")
	}
	// Replacing a file by rename over it.
	other, _ := es.AddFile(movieDir.ID, "old.mkv", "movie:tt1:old")
	if err := es.Rename(moved.ID, movieDir.ID, "old.mkv"); err != nil {
		t.Fatal(err)
	}
	if _, err := es.Remove(other.ID), es.Remove(other.ID); !errors.Is(err, ErrExportNotFound) {
		t.Fatalf("replaced target should already be gone: %v", err)
	}
	nodes, _ = es.Nodes()
	names := map[string]bool{}
	for _, n := range nodes {
		names[n.Name] = true
	}
	if !names["old.mkv"] || names["Iron Man (2008) Bluray-2160p.mkv"] || !names["sub"] {
		t.Fatalf("final tree: %v", names)
	}
}

func TestExportStoreScratchFiles(t *testing.T) {
	es := openTestManager(t).ExportStore()
	if err := es.EnsureRoots(); err != nil {
		t.Fatal(err)
	}
	f, err := es.AddScratch(0, "note.nfo")
	if err != nil {
		t.Fatal(err)
	}
	if !f.IsScratch() {
		t.Fatal("scratch file must have no item")
	}
	if err := es.WriteScratch(f.ID, 0, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := es.WriteScratch(f.ID, 8, []byte("!")); err != nil {
		t.Fatal(err)
	}
	got, _, _ := es.Child(0, "note.nfo")
	if string(got.Data) != "hello\x00\x00\x00!" {
		t.Fatalf("data %q", got.Data)
	}
	if err := es.WriteScratch(f.ID, 2, nil); err != nil {
		t.Fatal(err)
	}
	got, _, _ = es.Child(0, "note.nfo")
	if string(got.Data) != "he" {
		t.Fatalf("truncated data %q", got.Data)
	}
	if err := es.WriteScratch(f.ID, MaxScratchBytes, []byte("x")); !errors.Is(err, ErrExportTooLarge) {
		t.Fatalf("cap: %v", err)
	}
	alias, _ := es.AddFile(0, "movie.mkv", "movie:tt1:x")
	if err := es.WriteScratch(alias.ID, 0, []byte("x")); !errors.Is(err, ErrExportNotFile) {
		t.Fatalf("writing an alias: %v", err)
	}
	if _, err := es.AddScratch(0, "movie.mkv"); !errors.Is(err, ErrExportExists) {
		t.Fatalf("scratch over an alias: %v", err)
	}
	nodes, _ := es.Nodes()
	for _, n := range nodes {
		if n.Name == "note.nfo" && string(n.Data) != "he" {
			t.Fatal("Nodes must carry scratch data")
		}
	}
}

func TestExportReplacementRollsBack(t *testing.T) {
	m := (testDB{settings: Settings{Backend: BackendSQLite}, dataDir: t.TempDir()}).open(t)
	t.Cleanup(func() { _ = m.Close() })
	es := m.ExportStore()
	dst, err := es.AddFile(0, "destination", "old")
	if err != nil {
		t.Fatal(err)
	}
	src, err := es.AddFile(0, "source", "new")
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"INSERT", "UPDATE"} {
		_, err := es.wdb.Exec("CREATE TRIGGER reject_" + operation + " BEFORE " + operation + " ON export_nodes WHEN NEW.name = 'destination' BEGIN SELECT RAISE(ABORT, 'simulated failure'); END")
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := es.AddFile(0, "destination", "replacement"); err == nil {
		t.Fatal("replacement succeeded")
	}
	if err := es.Rename(src.ID, 0, "destination"); err == nil {
		t.Fatal("rename succeeded")
	}
	for _, want := range []ExportNode{src, dst} {
		got, ok, err := es.Child(0, want.Name)
		if err != nil || !ok || got.ID != want.ID || got.ItemID != want.ItemID {
			t.Fatalf("lost %s after rollback: %+v, %v", want.Name, got, err)
		}
	}
}

func TestExportStoreRemoveTree(t *testing.T) {
	es := openTestManager(t).ExportStore()
	_ = es.EnsureRoots()
	downloads, _, _ := es.Child(0, ExportRootDownloads)
	job, _ := es.Mkdir(downloads.ID, "job")
	sub, _ := es.Mkdir(job.ID, "sub")
	_, _ = es.AddFile(job.ID, "a.mkv", "movie:gone:a")
	if err := es.RemoveTree(job.ID); !errors.Is(err, ErrExportNotEmpty) {
		t.Fatalf("removed live subdirectory: %v", err)
	}
	if err := es.Remove(sub.ID); err != nil {
		t.Fatal(err)
	}
	if err := es.Remove(job.ID); !errors.Is(err, ErrExportNotEmpty) {
		t.Fatalf("plain remove must refuse: %v", err)
	}
	if err := es.RemoveTree(job.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := es.Child(downloads.ID, "job"); ok {
		t.Fatal("tree still present")
	}
	nodes, _ := es.Nodes()
	if len(nodes) != 1 {
		t.Fatalf("expected only the downloads root to remain, got %d nodes", len(nodes))
	}
	if err := es.RemoveTree(downloads.ID); !errors.Is(err, ErrExportRoot) {
		t.Fatalf("downloads must stay: %v", err)
	}
}
