package unpack

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"io"
	"testing"
)

func TestVirtualStreamLiveCount(t *testing.T) {
	before := LiveVirtualStreams()
	stream := NewVirtualStream(context.Background(), nil, 0, 0)
	if got := LiveVirtualStreams(); got != before+1 {
		_ = stream.Close()
		t.Fatalf("expected %d live virtual streams, got %d", before+1, got)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}
	if got := LiveVirtualStreams(); got != before {
		t.Fatalf("expected %d live virtual streams after close, got %d", before, got)
	}
}

func TestVirtualStreamReadsAcrossPartBoundaries(t *testing.T) {
	stream := NewVirtualStream(context.Background(), []virtualPart{
		{VirtualStart: 0, VirtualEnd: 3, VolFile: &memoryUnpackableFile{name: "part1", data: []byte("abc")}},
		{VirtualStart: 3, VirtualEnd: 7, VolFile: &memoryUnpackableFile{name: "part2", data: []byte("defg")}},
	}, 7, 0)
	defer stream.Close()

	got, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("ReadAll returned error: %v", err)
	}
	if string(got) != "abcdefg" {
		t.Fatalf("expected concatenated stream %q, got %q", "abcdefg", string(got))
	}
}

func TestVirtualStreamSeekNearEOFReturnsRemainingBytesThenEOF(t *testing.T) {
	stream := NewVirtualStream(context.Background(), []virtualPart{
		{VirtualStart: 0, VirtualEnd: 3, VolFile: &memoryUnpackableFile{name: "part1", data: []byte("abc")}},
		{VirtualStart: 3, VirtualEnd: 7, VolFile: &memoryUnpackableFile{name: "part2", data: []byte("defg")}},
	}, 7, 0)
	defer stream.Close()

	if _, err := stream.Seek(-2, io.SeekEnd); err != nil {
		t.Fatalf("Seek returned error: %v", err)
	}

	buf := make([]byte, 4)
	n, err := stream.Read(buf)
	if err != nil {
		t.Fatalf("Read returned error: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected to read 2 bytes near EOF, got %d", n)
	}
	if string(buf[:n]) != "fg" {
		t.Fatalf("expected remaining bytes %q, got %q", "fg", string(buf[:n]))
	}

	n, err = stream.Read(buf)
	if n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF after tail read, got (%d, %v)", n, err)
	}
}

func TestVirtualStreamSeekToEndReturnsEOFOnRead(t *testing.T) {
	stream := NewVirtualStream(context.Background(), []virtualPart{
		{VirtualStart: 0, VirtualEnd: 3, VolFile: &memoryUnpackableFile{name: "part1", data: []byte("abc")}},
		{VirtualStart: 3, VirtualEnd: 7, VolFile: &memoryUnpackableFile{name: "part2", data: []byte("defg")}},
	}, 7, 0)
	defer stream.Close()

	if _, err := stream.Seek(0, io.SeekEnd); err != nil {
		t.Fatalf("Seek returned error: %v", err)
	}

	buf := make([]byte, 1)
	n, err := stream.Read(buf)
	if n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("expected EOF after seek to end, got (%d, %v)", n, err)
	}
}

func TestEncryptedVirtualStreamCorrectDecryptionAndSeeking(t *testing.T) {
	// AES block size is 16. Use a 64-byte plaintext.
	plaintext := []byte("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	key := []byte("0123456789abcdef0123456789abcdef") // 32 bytes (AES-256)
	iv := []byte("fedcba9876543210")                  // 16 bytes

	// Encrypt plaintext using AES-256-CBC
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("NewCipher: %v", err)
	}
	ciphertext := make([]byte, len(plaintext))
	mode := cipher.NewCBCEncrypter(block, iv)
	mode.CryptBlocks(ciphertext, plaintext)

	// Split ciphertext into two virtual parts:
	// Part 1: virtual offsets [0, 30), file name "vol1"
	// Part 2: virtual offsets [30, 64), file name "vol2"
	parts := []virtualPart{
		{
			VirtualStart: 0,
			VirtualEnd:   30,
			VolFile:      &memoryUnpackableFile{name: "vol1", data: ciphertext[:30]},
			VolOffset:    0,
			AesKey:       key,
			AesIV:        iv,
		},
		{
			VirtualStart: 30,
			VirtualEnd:   64,
			VolFile:      &memoryUnpackableFile{name: "vol2", data: ciphertext[30:]},
			VolOffset:    0,
			AesKey:       key,
			AesIV:        iv,
		},
	}

	stream := NewEncryptedVirtualStream(context.Background(), parts, int64(len(plaintext)), int64(len(ciphertext)), key, iv, 0)
	defer stream.Close()

	// 1. Verify sequential Read
	buf := make([]byte, len(plaintext))
	if _, err := io.ReadFull(stream, buf); err != nil {
		t.Fatalf("io.ReadFull: %v", err)
	}
	if !bytes.Equal(buf, plaintext) {
		t.Fatalf("decrypted sequential output mismatch:\ngot:  %q\nwant: %q", string(buf), string(plaintext))
	}

	// 2. Verify Seek and Read (instant random-access decryption)
	seeks := []struct {
		offset int64
		length int
		want   string
	}{
		{offset: 0, length: 16, want: "0123456789abcdef"},
		{offset: 16, length: 16, want: "0123456789abcdef"},
		{offset: 32, length: 16, want: "0123456789abcdef"},
		{offset: 48, length: 16, want: "0123456789abcdef"},
		// Unaligned seek & read
		{offset: 5, length: 10, want: "56789abcde"},
		{offset: 25, length: 15, want: "9abcdef01234567"},
		{offset: 40, length: 20, want: "89abcdef0123456789ab"},
	}

	for _, tc := range seeks {
		if _, err := stream.Seek(tc.offset, io.SeekStart); err != nil {
			t.Fatalf("Seek to %d: %v", tc.offset, err)
		}
		readBuf := make([]byte, tc.length)
		if _, err := io.ReadFull(stream, readBuf); err != nil {
			t.Fatalf("ReadFull at offset %d: %v", tc.offset, err)
		}
		if string(readBuf) != tc.want {
			t.Errorf("at offset %d: got %q, want %q", tc.offset, string(readBuf), tc.want)
		}
	}
}
