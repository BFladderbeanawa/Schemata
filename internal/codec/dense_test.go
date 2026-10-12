package codec

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math/big"
	"strings"
	"testing"

	"github.com/bfladderbeanawa/schemata/internal/alphabet"
	"github.com/bfladderbeanawa/schemata/internal/layout"
	"github.com/bfladderbeanawa/schemata/internal/volume"
)

func TestDenseByteExactBoundaries(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		size int
	}{
		{name: "empty", size: 0}, {name: "short", size: 1}, {name: "filename boundary", size: 249},
		{name: "full chunk", size: 250}, {name: "next chunk", size: 251}, {name: "many chunks", size: 4097},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := make([]byte, tc.size)
			for i := range payload {
				payload[i] = byte(i * 37)
			}
			if len(payload) > 0 {
				payload[len(payload)-1] = 0
			}
			v, err := EncodeDense(payload, "é.bin", DenseOptions{})
			if err != nil {
				t.Fatal(err)
			}
			for _, workers := range []int{1, 4, 256} {
				file, err := Decode(context.Background(), v, workers)
				if err != nil {
					t.Fatal(err)
				}
				if file.Name != "é.bin" || !bytes.Equal(file.Payload, payload) {
					t.Fatalf("workers %d: recovery differs", workers)
				}
			}
		})
	}
}

func TestDenseHeaderFilenameCrossesChunkBoundary(t *testing.T) {
	t.Parallel()
	name := strings.Repeat("x", 253) + ".bin"
	payload := []byte{0, 0, 173, 255, 0, 0}
	v, err := EncodeDense(payload, name, DenseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, workers := range []int{1, 4, 256} {
		file, err := Decode(context.Background(), v, workers)
		if err != nil || file.Name != name || !bytes.Equal(file.Payload, payload) {
			t.Fatalf("workers %d: crossing filename: %v", workers, err)
		}
	}
}

func TestDenseConversionPreservesZerosAndRejectsUnusedCombinations(t *testing.T) {
	t.Parallel()
	g := denseGeometry()
	for size := 1; size <= denseChunkBytes; size++ {
		data := make([]byte, size)
		if size > 2 {
			data[1] = 255
		}
		digits := bytesToSymbols(data, g)
		got := make([]byte, size)
		if err := symbolsToBytes(got, digits, g); err != nil || !bytes.Equal(got, data) {
			t.Fatalf("size %d: %x, %v", size, got, err)
		}
	}
	invalid := make([]*big.Int, g.digits[1])
	for i := range invalid {
		invalid[i] = new(big.Int).Sub(g.base, big.NewInt(1))
	}
	if err := symbolsToBytes(make([]byte, 1), invalid, g); err == nil {
		t.Fatal("unused numeric combination accepted")
	}
}

func TestDenseRejectsCorruptionAndImpossibleLengths(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		alter func(*volume.Volume)
		want  string
	}{
		{name: "unknown version", alter: func(v *volume.Volume) { denseSetHeaderByte(t, v, 8, 4) }, want: "unsupported encoding version"},
		{name: "payload over limit", alter: func(v *volume.Volume) {
			var b [8]byte
			binary.BigEndian.PutUint64(b[:], MaxDensePayload+1)
			for i, x := range b {
				denseSetHeaderByte(t, v, 9+i, x)
			}
		}, want: "32 MiB"},
		{name: "impossible name length", alter: func(v *volume.Volume) { denseSetHeaderByte(t, v, 17, 255); denseSetHeaderByte(t, v, 18, 255) }, want: "truncated"},
		{name: "payload checksum", alter: func(v *volume.Volume) { denseSetHeaderByte(t, v, 19, 255) }, want: "checksum"},
		{name: "truncated blocks", alter: func(v *volume.Volume) { v.Blocks = v.Blocks[:len(v.Blocks)-1] }, want: "block count"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, err := EncodeDense([]byte{0, 1, 2, 0}, "x.bin", DenseOptions{})
			if err != nil {
				t.Fatal(err)
			}
			tc.alter(&v)
			for _, workers := range []int{1, 4} {
				if _, err := Decode(context.Background(), v, workers); err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("error %v, want %q", err, tc.want)
				}
			}
		})
	}
}

func TestDensePaddingAndUnsafeName(t *testing.T) {
	t.Parallel()
	v, err := EncodeDense([]byte{0, 0}, "x.bin", DenseOptions{Layout: layout.Options{Policy: "balanced"}})
	if err != nil {
		t.Fatal(err)
	}
	_, used, err := planDenseChunks([]byte{0, 0}, "x.bin", denseGeometry(), ordinaryFallback(), false)
	if err != nil {
		t.Fatal(err)
	}
	if used >= len(v.Blocks) {
		t.Fatal("fixture has no padding")
	}
	v.Blocks[used] = v.Blocks[0]
	if _, err := Decode(context.Background(), v, 4); err == nil || !strings.Contains(err.Error(), "padding") {
		t.Fatalf("invalid padding: %v", err)
	}
	for _, name := range []string{"../x", "..", "/root", "a\\b"} {
		if _, err := EncodeDense(nil, name, DenseOptions{}); err == nil {
			t.Fatalf("unsafe name %q accepted", name)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Decode(ctx, v, 4); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func denseSetHeaderByte(t *testing.T, v *volume.Volume, offset int, b byte) {
	t.Helper()
	for half, nibble := range []byte{b >> 4, b & 15} {
		state := volume.State{Name: "minecraft:" + concreteNames[nibble] + "_concrete"}
		v.Palette = append(v.Palette, state)
		v.Blocks[2*offset+half] = uint32(len(v.Palette) - 1)
	}
}

func BenchmarkDenseChunkConversion(b *testing.B) {
	g := denseGeometry()
	data := make([]byte, denseChunkBytes)
	for i := range data {
		data[i] = byte(i*37 + 11)
	}
	digits := bytesToSymbols(data, g)
	b.Run("encode256", func(b *testing.B) {
		b.SetBytes(denseChunkBytes)
		for b.Loop() {
			bytesToSymbols(data, g)
		}
	})
	b.Run("decode256", func(b *testing.B) {
		b.SetBytes(denseChunkBytes)
		for b.Loop() {
			if err := symbolsToBytes(data, digits, g); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Logf("alphabet=%s, ordinary=%d, full-chunk symbols=%d", alphabet.Count(), len(alphabet.States()), g.digits[denseChunkBytes])
}
