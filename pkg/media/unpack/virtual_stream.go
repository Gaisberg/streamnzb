package unpack

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

type virtualPart struct {
	VirtualStart int64
	VirtualEnd   int64
	VolFile      UnpackableFile
	VolOffset    int64
	AesKey       []byte
	AesIV        []byte
}

type VirtualStream struct {
	parts     []virtualPart
	totalSize int64
	ctx       context.Context

	mu            sync.Mutex
	offset        int64
	currentReader io.ReadCloser
	currentPart   int
	closed        bool
}

var liveVirtualStreams atomic.Int64

func LiveVirtualStreams() int64 {
	return liveVirtualStreams.Load()
}

func NewVirtualStream(ctx context.Context, parts []virtualPart, totalSize int64, startOffset int64) *VirtualStream {
	liveVirtualStreams.Add(1)
	return &VirtualStream{
		parts:       parts,
		totalSize:   totalSize,
		ctx:         ctx,
		offset:      startOffset,
		currentPart: -1,
	}
}

func (s *VirtualStream) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}

	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return 0, io.ErrClosedPipe
		}

		if s.offset >= s.totalSize {
			s.mu.Unlock()
			return 0, io.EOF
		}

		select {
		case <-s.ctx.Done():
			s.mu.Unlock()
			return 0, s.ctx.Err()
		default:
		}

		part, partIdx := s.findPart(s.offset)
		if part == nil {
			s.mu.Unlock()
			return 0, fmt.Errorf("offset %d not mapped in %d parts", s.offset, len(s.parts))
		}

		if err := s.ensureReader(part, partIdx); err != nil {
			s.mu.Unlock()
			return 0, err
		}

		remaining := part.VirtualEnd - s.offset
		buf := p
		if int64(len(buf)) > remaining {
			buf = buf[:remaining]
		}

		expectedOffset := s.offset
		reader := s.currentReader
		s.mu.Unlock()

		n, err := reader.Read(buf)

		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return n, io.ErrClosedPipe
		}

		if s.offset != expectedOffset || s.currentReader != reader {
			s.mu.Unlock()
			return 0, fmt.Errorf("virtual stream: concurrent seek or read detected")
		}

		s.offset += int64(n)

		if err == io.EOF {
			if s.currentReader == reader {
				s.closeReader()
			}

			if n == 0 && s.offset < part.VirtualEnd {
				// Exhausted this volume before the packed span ends (trailing RAR bytes).
				// Advance to the next virtual part instead of stalling on a dead reader.
				s.offset = part.VirtualEnd
			}
			if n > 0 {
				if s.offset >= part.VirtualEnd && s.offset < s.totalSize {
					s.prefetchPart(partIdx + 1)
				}
				s.mu.Unlock()
				return n, nil
			}
			if s.offset < s.totalSize {
				s.mu.Unlock()
				continue
			}
			s.mu.Unlock()
			return 0, io.EOF
		}

		if s.offset >= part.VirtualEnd && s.offset < s.totalSize {
			s.prefetchPart(partIdx + 1)
		}

		s.mu.Unlock()
		return n, err
	}
}

func (s *VirtualStream) Seek(offset int64, whence int) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return 0, io.ErrClosedPipe
	}

	var target int64
	switch whence {
	case io.SeekStart:
		target = offset
	case io.SeekCurrent:
		target = s.offset + offset
	case io.SeekEnd:
		target = s.totalSize + offset
	default:
		return 0, errors.New("invalid whence")
	}

	if target < 0 || target > s.totalSize {
		return 0, errors.New("seek out of bounds")
	}

	if target == s.offset {
		return target, nil
	}

	part, partIdx := s.findPart(target)
	if part != nil && s.currentReader != nil && s.currentPart == partIdx {
		localOff := target - part.VirtualStart
		volOff := part.VolOffset + localOff

		if seeker, ok := s.currentReader.(io.Seeker); ok {
			_, err := seeker.Seek(volOff, io.SeekStart)
			if err == nil {
				s.offset = target
				return target, nil
			}
		}
	}

	s.closeReader()
	s.offset = target
	return target, nil
}

func (s *VirtualStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}
	s.closed = true
	s.closeReader()
	liveVirtualStreams.Add(-1)
	return nil
}

func (s *VirtualStream) findPart(offset int64) (*virtualPart, int) {
	left, right := 0, len(s.parts)-1
	for left <= right {
		mid := (left + right) / 2
		p := &s.parts[mid]
		if offset >= p.VirtualStart && offset < p.VirtualEnd {
			return p, mid
		}
		if offset < p.VirtualStart {
			right = mid - 1
		} else {
			left = mid + 1
		}
	}
	return nil, -1
}

func (s *VirtualStream) ensureReader(part *virtualPart, partIdx int) error {
	if s.currentReader != nil && s.currentPart == partIdx {
		return nil
	}

	s.closeReader()

	localOff := s.offset - part.VirtualStart
	volOff := part.VolOffset + localOff

	r, err := openPlaybackReaderAt(part.VolFile, playbackSegmentMapCtx(s.ctx), volOff)
	if err != nil {
		return fmt.Errorf("open volume part %d at offset %d: %w", partIdx, volOff, err)
	}

	s.currentReader = r
	s.currentPart = partIdx
	return nil
}

func (s *VirtualStream) prefetchPart(partIdx int) {
	if partIdx < 0 || partIdx >= len(s.parts) {
		return
	}
	part := &s.parts[partIdx]
	if prefetcher, ok := part.VolFile.(playbackPrefetcher); ok {
		prefetcher.PrefetchPlaybackOffset(playbackSegmentMapCtx(s.ctx), part.VolOffset)
	}
}

func (s *VirtualStream) closeReader() {
	if s.currentReader != nil {
		s.currentReader.Close()
		s.currentReader = nil
		s.currentPart = -1
	}
}

type EncryptedVirtualStream struct {
	source     *VirtualStream
	totalSize  int64 // Unpacked size
	packedSize int64 // Ciphertext size
	aesKey     []byte
	aesIV      []byte
	offset     int64
	mu         sync.Mutex
	closed     bool
}

func NewEncryptedVirtualStream(
	ctx context.Context,
	parts []virtualPart,
	totalSize int64,
	packedSize int64,
	aesKey []byte,
	aesIV []byte,
	startOffset int64,
) *EncryptedVirtualStream {
	source := NewVirtualStream(ctx, parts, packedSize, 0)
	return &EncryptedVirtualStream{
		source:     source,
		totalSize:  totalSize,
		packedSize: packedSize,
		aesKey:     aesKey,
		aesIV:      aesIV,
		offset:     startOffset,
	}
}

func (s *EncryptedVirtualStream) Read(p []byte) (int, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	if s.offset >= s.totalSize {
		s.mu.Unlock()
		return 0, io.EOF
	}

	n := len(p)
	if n == 0 {
		s.mu.Unlock()
		return 0, nil
	}

	// Clamp the requested size to the remaining unpacked bytes
	if s.offset+int64(n) > s.totalSize {
		n = int(s.totalSize - s.offset)
	}
	if n <= 0 {
		s.mu.Unlock()
		return 0, io.EOF
	}

	alignedStart := s.offset - (s.offset % 16)
	alignedEnd := s.offset + int64(n)
	// Align alignedEnd to 16 bytes
	if alignedEnd%16 != 0 {
		alignedEnd = (alignedEnd/16 + 1) * 16
	}
	if alignedEnd > s.packedSize {
		alignedEnd = s.packedSize
	}

	expectedOffset := s.offset
	s.mu.Unlock()

	// 1. Obtain the IV (no lock held)
	var iv []byte
	if alignedStart == 0 {
		iv = make([]byte, 16)
		copy(iv, s.aesIV)
	} else {
		iv = make([]byte, 16)
		if _, err := s.source.Seek(alignedStart-16, io.SeekStart); err != nil {
			return 0, fmt.Errorf("seek to IV offset %d: %w", alignedStart-16, err)
		}
		if _, err := io.ReadFull(s.source, iv); err != nil {
			return 0, fmt.Errorf("read IV at offset %d: %w", alignedStart-16, err)
		}
	}

	// 2. Read the ciphertext (no lock held)
	cipherLen := alignedEnd - alignedStart
	if cipherLen <= 0 {
		return 0, io.EOF
	}
	ciphertext := make([]byte, cipherLen)
	if _, err := s.source.Seek(alignedStart, io.SeekStart); err != nil {
		return 0, fmt.Errorf("seek to ciphertext offset %d: %w", alignedStart, err)
	}
	if _, err := io.ReadFull(s.source, ciphertext); err != nil {
		return 0, fmt.Errorf("read ciphertext at offset %d: %w", alignedStart, err)
	}

	// 3. Decrypt ciphertext (no lock held)
	block, err := aes.NewCipher(s.aesKey)
	if err != nil {
		return 0, fmt.Errorf("new aes cipher: %w", err)
	}
	decryptLen := (cipherLen / 16) * 16
	if decryptLen > 0 {
		mode := cipher.NewCBCDecrypter(block, iv)
		mode.CryptBlocks(ciphertext[:decryptLen], ciphertext[:decryptLen])
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	if s.offset != expectedOffset {
		s.mu.Unlock()
		return 0, fmt.Errorf("encrypted virtual stream: concurrent seek or read detected")
	}

	// 4. Copy to user buffer
	startInBuf := s.offset - alignedStart
	copied := copy(p[:n], ciphertext[startInBuf:])
	s.offset += int64(copied)

	s.mu.Unlock()
	return copied, nil
}

func (s *EncryptedVirtualStream) Seek(offset int64, whence int) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return 0, io.ErrClosedPipe
	}

	var target int64
	switch whence {
	case io.SeekStart:
		target = offset
	case io.SeekCurrent:
		target = s.offset + offset
	case io.SeekEnd:
		target = s.totalSize + offset
	default:
		return 0, errors.New("invalid whence")
	}

	if target < 0 || target > s.totalSize {
		return 0, errors.New("seek out of bounds")
	}

	s.offset = target
	return target, nil
}

func (s *EncryptedVirtualStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}
	s.closed = true
	return s.source.Close()
}
