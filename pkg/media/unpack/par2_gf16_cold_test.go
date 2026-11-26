package unpack

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/binary"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"
)

const gf16ColdChildEnv = "STREAMNZB_GF16_COLD_CHILD"

// runGF16Cold re-executes the named test in a fresh process so the GF16 tables
// are provably uninitialized. The child gets its own -test.timeout (goroutine
// dump on hang) and the parent kills it if that fails, so a regression of the
// recursive sync.Once deadlock fails fast instead of hanging the suite.
func runGF16Cold(t *testing.T, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^"+name+"$", "-test.timeout=15s", "-test.v")
	cmd.Env = append(os.Environ(), gf16ColdChildEnv+"=1")
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cold child failed (ctx err: %v): %v\n%s", ctx.Err(), err, out)
	}
}

// requireColdGF16 fails the child unless nothing has built the tables yet.
func requireColdGF16(t *testing.T) {
	t.Helper()
	if gf16ALog[1] != 0 || gf16Log[gf16Gen] != 0 {
		t.Fatal("GF16 tables already initialized; not a cold start")
	}
}

// gf16RefMul is a test-local carry-less multiply, independent of the
// production tables and helpers.
func gf16RefMul(a, b uint16) uint16 {
	var res uint32
	for i := 0; i < 16; i++ {
		if b&(1<<i) != 0 {
			res ^= uint32(a) << i
		}
	}
	for i := 31; i >= 16; i-- {
		if res&(1<<i) != 0 {
			res ^= 0x1100b << (i - 16)
		}
	}
	return uint16(res)
}

func TestPAR2ColdStartReconstruction(t *testing.T) {
	if os.Getenv(gf16ColdChildEnv) == "" {
		runGF16Cold(t, "TestPAR2ColdStartReconstruction")
		return
	}
	requireColdGF16(t)

	const sliceSize = 64
	vol1 := make([]byte, sliceSize)
	vol2 := make([]byte, sliceSize)
	for i := range vol1 {
		vol1[i] = byte(i*37 + 11)
		vol2[i] = byte(i*91 + 200)
	}
	// Recovery exponent 1 over slices 0 (vol1) and 1 (vol2): R = g^0*d1 ^ g^1*d2.
	recv := make([]byte, sliceSize)
	for w := 0; w < sliceSize; w += 2 {
		d1 := binary.LittleEndian.Uint16(vol1[w:])
		d2 := binary.LittleEndian.Uint16(vol2[w:])
		binary.LittleEndian.PutUint16(recv[w:], d1^gf16RefMul(gf16Gen, d2))
	}

	id1, id2 := [16]byte{1}, [16]byte{2}
	var pkts bytes.Buffer
	var mainBody bytes.Buffer
	binary.Write(&mainBody, binary.LittleEndian, uint64(sliceSize))
	binary.Write(&mainBody, binary.LittleEndian, uint32(2))
	mainBody.Write(id1[:])
	mainBody.Write(id2[:])
	writePAR2Packet(&pkts, par2MainPacketHeader, mainBody.Bytes())
	for _, f := range []struct {
		id   [16]byte
		data []byte
		name string
	}{{id1, vol1, "cold.part01.rar"}, {id2, vol2, "cold.part02.rar"}} {
		sum := md5.Sum(f.data)
		var b bytes.Buffer
		b.Write(f.id[:])
		b.Write(sum[:])
		b.Write(make([]byte, 16))
		binary.Write(&b, binary.LittleEndian, uint64(len(f.data)))
		b.WriteString(f.name + "\x00")
		writePAR2Packet(&pkts, par2FileDescPacket, b.Bytes())
	}
	var rb bytes.Buffer
	binary.Write(&rb, binary.LittleEndian, uint32(1))
	rb.Write(recv)
	writePAR2Packet(&pkts, par2RecvPacketHeader, rb.Bytes())

	got, err := RepairFirstVolumeWithPAR2(context.Background(), []UnpackableFile{
		&memoryUnpackableFile{name: "cold.part02.rar", data: vol2},
		&memoryUnpackableFile{name: "cold.par2", data: pkts.Bytes()},
	})
	if err != nil {
		t.Fatalf("RepairFirstVolumeWithPAR2: %v", err)
	}
	buf := make([]byte, sliceSize)
	rs, err := got.OpenStream()
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	if _, err := rs.Read(buf); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf, vol1) {
		t.Fatalf("reconstructed %x, want %x", buf, vol1)
	}
}

func TestGF16ConcurrentColdFirstUse(t *testing.T) {
	if os.Getenv(gf16ColdChildEnv) == "" {
		runGF16Cold(t, "TestGF16ConcurrentColdFirstUse")
		return
	}
	requireColdGF16(t)

	const workers = 32
	start := make(chan struct{})
	errs := make(chan string, workers)
	var wg sync.WaitGroup
	for g := 0; g < workers; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			<-start
			for i := 0; i < 2000; i++ {
				a := uint16(i*7919 + g + 1)
				b := uint16(i*104729 + 3)
				if a == 0 || b == 0 {
					continue
				}
				p := gf16Mul(a, b)
				if p != gf16RefMul(a, b) {
					errs <- "gf16Mul mismatch"
					return
				}
				if gf16Div(p, b) != a {
					errs <- "gf16Div mismatch"
					return
				}
				if gf16Pow(a, 3) != gf16RefMul(a, gf16RefMul(a, a)) {
					errs <- "gf16Pow mismatch"
					return
				}
			}
		}(g)
	}
	close(start)
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}
