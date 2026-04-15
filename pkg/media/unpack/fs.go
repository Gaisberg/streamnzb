package unpack

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"time"
)

type NZBFS struct {
	ctx   context.Context
	files map[string]UnpackableFile
}

func NewNZBFSFromMap(files map[string]UnpackableFile) *NZBFS {
	return NewNZBFSFromMapCtx(context.Background(), files)
}

func NewNZBFSFromMapCtx(ctx context.Context, files map[string]UnpackableFile) *NZBFS {
	if ctx == nil {
		ctx = context.Background()
	}
	return &NZBFS{ctx: ctx, files: files}
}

func (n *NZBFS) Open(name string) (fs.File, error) {
	f, ok := n.files[name]
	if !ok {
		f, ok = n.files[filepath.Base(name)]
	}
	if !ok {
		return nil, fs.ErrNotExist
	}

	stream, err := f.OpenStreamCtx(n.ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to open stream: %w", err)
	}

	return &fileWrapper{
		ctx:    n.ctx,
		stream: stream,
		file:   f,
		name:   ExtractFilename(f.Name()),
		size:   f.Size(),
	}, nil
}

type fileWrapper struct {
	ctx    context.Context
	stream io.ReadSeekCloser
	file   UnpackableFile
	name   string
	size   int64
}

func (fw *fileWrapper) Stat() (fs.FileInfo, error) {
	return &fileInfo{name: fw.name, size: fw.size}, nil
}

func (fw *fileWrapper) Read(p []byte) (int, error)                { return fw.stream.Read(p) }
func (fw *fileWrapper) Seek(off int64, whence int) (int64, error) { return fw.stream.Seek(off, whence) }
func (fw *fileWrapper) Close() error                              { return fw.stream.Close() }
func (fw *fileWrapper) ReadAt(p []byte, off int64) (int, error) {
	reader, err := fw.file.OpenReaderAt(fw.ctx, off)
	if err != nil {
		return 0, err
	}
	defer reader.Close()

	n, readErr := io.ReadFull(reader, p)
	if readErr == io.ErrUnexpectedEOF {
		return n, io.EOF
	}
	return n, readErr
}

type fileInfo struct {
	name string
	size int64
}

func (fi *fileInfo) Name() string       { return fi.name }
func (fi *fileInfo) Size() int64        { return fi.size }
func (fi *fileInfo) Mode() fs.FileMode  { return 0444 }
func (fi *fileInfo) ModTime() time.Time { return time.Time{} }
func (fi *fileInfo) IsDir() bool        { return false }
func (fi *fileInfo) Sys() interface{}   { return nil }
