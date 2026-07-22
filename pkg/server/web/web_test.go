package web

import (
	"io/fs"
	"testing"
)

func TestStaticFiles(t *testing.T) {
	err := fs.WalkDir(staticFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		t.Logf("Embedded file: %s (isDir: %v)", path, d.IsDir())
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir failed: %v", err)
	}
}
