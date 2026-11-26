package nfs

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"streamnzb/pkg/core/logger"
)

// NFS has no open and close: every READ names a handle, an offset and a
// length, and the client expects the server to remember nothing between two
// of them. The playback stream underneath is the opposite — a seekable
// reader whose open costs seconds and which prefetches ahead of the last
// read. The file cache bridges the two: the first READ on a handle opens the
// stream and later READs reuse it, seeking only when the client jumps. A
// stream nobody has read from for idleFileTTL is closed, which is also what
// ends the play on the dashboard.
//
// The open runs on its own goroutine so a slow one never holds a connection's
// other calls. A READ that arrives while it is still going waits for it, up
// to openWait, then answers errOpening, which the NFS layer turns into
// NFS3ERR_JUKEBOX: "working on it, ask again".

const (
	idleFileTTL   = 5 * time.Minute
	maxOpenFiles  = 64
	reapInterval  = 30 * time.Second
	openTimeout   = 2 * time.Minute
	defaultRtPref = 1 << 20
)

// openWait is how long a READ waits on an open in progress before it answers
// JUKEBOX instead. Long, deliberately: a cold open takes one to a few
// seconds and ordinary NFS servers simply block that long, which every
// client copes with. JUKEBOX is only the fallback for a genuinely slow open —
// Linux retries it, but the Windows client treats it as failure and
// re-mounts, so it must not be the normal cold-open answer. A variable so
// tests can shorten it.
var openWait = 15 * time.Second

// errOpening reports a file whose stream is still being opened.
var errOpening = errors.New("file is still opening")

type openKey struct {
	stream streamKey
	node   nodeID
}

type openFile struct {
	mu     sync.Mutex
	rs     io.ReadSeekCloser
	size   int64
	pos    int64
	closed bool
	// opening is non-nil while an open is in flight and closed when it
	// ends; openErr is its outcome.
	opening chan struct{}
	openErr error
	// cancelCtx ends the context the stream was opened under. The playback
	// stream stays bound to it for its whole life, so it is cancelled on
	// close, never when the open returns.
	cancelCtx context.CancelFunc
	// lastUsed is unix nanoseconds, read by the reaper and the evictor
	// without taking mu, which a read in progress holds for seconds.
	lastUsed atomic.Int64
}

func (f *openFile) touch(now time.Time) { f.lastUsed.Store(now.UnixNano()) }

func (f *openFile) idleSince(now time.Time) time.Duration {
	return now.Sub(time.Unix(0, f.lastUsed.Load()))
}

// openFunc opens the byte stream for one file. It is given to the cache per
// read so the caller, who knows the stream and the library row, stays the
// one that decides how a file opens.
type openFunc func(ctx context.Context) (io.ReadSeekCloser, int64, error)

type fileCache struct {
	mu    sync.Mutex
	files map[openKey]*openFile
}

func newFileCache() *fileCache {
	return &fileCache{files: make(map[openKey]*openFile)}
}

// readAt returns up to count bytes at off, and whether that reached the end
// of the file. A read past the end returns no bytes and eof. Reads on one
// file are serialised; reads on different files run concurrently. While the
// file's stream is still being opened it returns errOpening.
func (fc *fileCache) readAt(ctx context.Context, key openKey, open openFunc, off int64, count int) ([]byte, bool, error) {
	f := fc.entry(key)
	f.mu.Lock()
	if f.closed {
		// Reaped between lookup and lock; take a fresh entry.
		f.mu.Unlock()
		f = fc.entry(key)
		f.mu.Lock()
	}
	f.touch(time.Now())
	if f.rs == nil && f.opening == nil {
		f.startOpenLocked(ctx, open)
	}
	opening := f.opening
	f.mu.Unlock()

	if opening != nil {
		select {
		case <-opening:
		case <-time.After(openWait):
			return nil, false, errOpening
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rs == nil {
		err := f.openErr
		if err == nil {
			err = errors.New("file did not open")
		}
		fc.drop(key, f)
		return nil, false, err
	}
	f.touch(time.Now())

	if off >= f.size {
		return nil, true, nil
	}
	if remaining := f.size - off; int64(count) > remaining {
		count = int(remaining)
	}
	if f.pos != off {
		if _, err := f.rs.Seek(off, io.SeekStart); err != nil {
			fc.drop(key, f)
			return nil, false, err
		}
		f.pos = off
	}
	buf := make([]byte, count)
	n, err := io.ReadFull(f.rs, buf)
	f.pos += int64(n)
	switch {
	case err == nil:
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		// The stream ended before the size said it would. Report what was
		// read as the end; the client sees a shorter file, not an error.
		// Said in the log, because a client reading an empty file has no
		// other way to tell this from a genuinely short one.
		logger.Debug("NFS read hit the end of the stream early", "offset", off, "read", n, "size", f.size, "err", err)
		if n == 0 {
			fc.drop(key, f)
			return nil, true, nil
		}
	default:
		// A failed stream is not reused: the next read opens afresh, which
		// is also how a session the manager evicted underneath us recovers.
		fc.drop(key, f)
		return nil, false, err
	}
	return buf[:n], f.pos >= f.size, nil
}

// startOpenLocked begins opening the stream on its own goroutine. Caller
// holds f.mu. The open is bound to the server's context, not the read's: the
// read that started it may give up and retry, and the open should carry on
// for the retry to find.
func (f *openFile) startOpenLocked(ctx context.Context, open openFunc) {
	done := make(chan struct{})
	f.opening = done
	go func() {
		// Not WithTimeout: the stream the open returns lives on this context
		// (its probe and read-ahead are bound to it), so a deadline that
		// outlived the open would kill it mid-play. The timer bounds only
		// the open itself; success hands the cancel to the file.
		openCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		timer := time.AfterFunc(openTimeout, cancel)
		openStart := time.Now()
		rs, size, err := open(openCtx)
		timer.Stop()
		if err == nil {
			logger.Debug("NFS file opened", "size", size, "ms", time.Since(openStart).Milliseconds())
		} else {
			logger.Debug("NFS file open failed", "ms", time.Since(openStart).Milliseconds(), "err", err)
		}
		f.mu.Lock()
		if f.closed {
			// Reaped or evicted while opening: nobody will read this.
			f.mu.Unlock()
			if rs != nil {
				_ = rs.Close()
			}
			cancel()
			close(done)
			return
		}
		if err == nil {
			f.rs, f.size, f.pos = rs, size, 0
			f.cancelCtx = cancel
		} else {
			cancel()
		}
		f.openErr = err
		f.opening = nil
		f.mu.Unlock()
		close(done)
	}()
}

// entry returns the cache slot for key, creating it if absent and making
// room if the cache is full.
func (fc *fileCache) entry(key openKey) *openFile {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	if f, ok := fc.files[key]; ok {
		return f
	}
	if len(fc.files) >= maxOpenFiles {
		fc.evictOldestLocked()
	}
	f := &openFile{}
	f.touch(time.Now())
	fc.files[key] = f
	return f
}

// evictOldestLocked closes the least recently used file that no read holds.
func (fc *fileCache) evictOldestLocked() {
	var oldestKey openKey
	var oldest *openFile
	for k, f := range fc.files {
		if oldest == nil || f.lastUsed.Load() < oldest.lastUsed.Load() {
			oldestKey, oldest = k, f
		}
	}
	if oldest == nil || !oldest.mu.TryLock() {
		return
	}
	defer oldest.mu.Unlock()
	delete(fc.files, oldestKey)
	oldest.closeLocked()
}

// drop removes f from the cache and closes it. Caller holds f.mu.
func (fc *fileCache) drop(key openKey, f *openFile) {
	fc.mu.Lock()
	if cur, ok := fc.files[key]; ok && cur == f {
		delete(fc.files, key)
	}
	fc.mu.Unlock()
	f.closeLocked()
}

func (f *openFile) closeLocked() {
	if f.closed {
		return
	}
	f.closed = true
	if f.rs != nil {
		if err := f.rs.Close(); err != nil {
			logger.Trace("NFS file close reported an error", "err", err)
		}
		f.rs = nil
	}
	if f.cancelCtx != nil {
		f.cancelCtx()
		f.cancelCtx = nil
	}
}

// closeKey closes one file if it is open, for a node that was removed.
func (fc *fileCache) closeKey(key openKey) {
	fc.mu.Lock()
	f, ok := fc.files[key]
	fc.mu.Unlock()
	if !ok {
		return
	}
	f.mu.Lock()
	fc.drop(key, f)
	f.mu.Unlock()
}

// reap closes files idle longer than idleFileTTL.
func (fc *fileCache) reap(now time.Time) int {
	fc.mu.Lock()
	var idle []openKey
	for k, f := range fc.files {
		if f.idleSince(now) > idleFileTTL {
			idle = append(idle, k)
		}
	}
	fc.mu.Unlock()
	closed := 0
	for _, k := range idle {
		fc.mu.Lock()
		f, ok := fc.files[k]
		fc.mu.Unlock()
		if !ok {
			continue
		}
		// A read in progress refreshes lastUsed; skip rather than wait.
		if !f.mu.TryLock() {
			continue
		}
		if f.idleSince(time.Now()) > idleFileTTL && f.opening == nil {
			fc.drop(k, f)
			closed++
		}
		f.mu.Unlock()
	}
	return closed
}

// closeAll closes every open file; used at shutdown.
func (fc *fileCache) closeAll() {
	fc.mu.Lock()
	files := fc.files
	fc.files = make(map[openKey]*openFile)
	fc.mu.Unlock()
	for _, f := range files {
		f.mu.Lock()
		f.closeLocked()
		f.mu.Unlock()
	}
}
