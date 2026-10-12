package codec_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"strings"
	"testing"
	"time"

	"github.com/bfladderbeanawa/schemata/internal/codec"
	"github.com/bfladderbeanawa/schemata/internal/layout"
	"github.com/bfladderbeanawa/schemata/internal/volume"
)

func TestEncodeGoldenEmptyHeader(t *testing.T) {
	// SCHEMATA, version 1, zero payload length, name length 1, zero CRC, x.
	want := []byte{
		0x53, 0x43, 0x48, 0x45, 0x4d, 0x41, 0x54, 0x41,
		0x01, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0x78,
	}
	got, err := codec.Encode(nil, "x", layout.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got.X != 16 || got.Y != 16 || got.Z != 16 {
		t.Fatalf("empty dimensions = %+v", got)
	}
	assertColors(t, got, want)
	file, err := codec.Decode(context.Background(), got, 256)
	if err != nil || file.Name != "x" || len(file.Payload) != 0 || file.Part != nil {
		t.Fatalf("empty decode = %+v, %v", file, err)
	}
}

func TestEncodeGoldenPayloadCRC(t *testing.T) {
	// CRC32/IEEE("123456789") = 0xcbf43926. The header checksum
	// must exclude the filename and all rectangular padding.
	want := []byte{
		0x53, 0x43, 0x48, 0x45, 0x4d, 0x41, 0x54, 0x41,
		0x01, 0, 0, 0, 0, 0, 0, 0, 9, 0, 1, 0xcb, 0xf4, 0x39, 0x26,
		'x', '1', '2', '3', '4', '5', '6', '7', '8', '9',
	}
	v, err := codec.Encode([]byte("123456789"), "x", layout.Options{})
	if err != nil {
		t.Fatal(err)
	}
	assertColors(t, v, want)
	file, err := codec.Decode(context.Background(), referenceVolume(16, 16, 16, want), 1)
	if err != nil || file.Name != "x" || string(file.Payload) != "123456789" {
		t.Fatalf("golden CRC decode = %+v, %v", file, err)
	}
}

func TestEncodeByteExactAndBoundaries(t *testing.T) {
	allBytes := make([]byte, 256)
	for i := range allBytes {
		allBytes[i] = byte(i)
	}
	tests := []struct {
		name     string
		filename string
		payload  []byte
	}{
		{name: "all byte values", filename: "bytes.bin", payload: allBytes},
		{name: "trailing zeros", filename: "zeros.bin", payload: []byte{0xff, 0, 0x80, 0, 0}},
		{name: "exact single unit", filename: "x", payload: patterned(2048 - 24)},
		{name: "payload starts on boundary", filename: strings.Repeat("n", 2048-23), payload: patterned(4097)},
		{name: "payload crosses boundary", filename: "x", payload: patterned(2048 - 23)},
		{name: "name crosses boundary", filename: strings.Repeat("n", 2048+7), payload: patterned(4099)},
		{name: "UTF8 crosses boundary", filename: strings.Repeat("n", 2048-24) + "é.dat", payload: allBytes},
		{name: "maximum filename bytes", filename: strings.Repeat("名", 21845), payload: patterned(8193)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, policy := range []string{"balanced", "compact", "bounded"} {
				t.Run(policy, func(t *testing.T) {
					v, err := codec.Encode(tt.payload, tt.filename, layout.Options{Policy: policy, PaddingPercent: 25})
					if err != nil {
						t.Fatal(err)
					}
					assertColors(t, v, referenceStream(tt.filename, tt.payload))
					for _, workers := range []int{1, 2, 7, 256} {
						file, err := codec.Decode(context.Background(), v, workers)
						if err != nil {
							t.Fatalf("workers %d: %v", workers, err)
						}
						if file.Name != tt.filename || !bytes.Equal(file.Payload, tt.payload) || file.Part != nil {
							t.Fatalf("workers %d: filename/payload differ", workers)
						}
					}
				})
			}
		})
	}
}

func TestEncodeFilenameSafety(t *testing.T) {
	invalid := []struct {
		name     string
		filename string
	}{
		{name: "empty", filename: ""},
		{name: "dot", filename: "."},
		{name: "dotdot", filename: ".."},
		{name: "slash", filename: "dir/file"},
		{name: "backslash", filename: "dir\\file"},
		{name: "unix absolute", filename: "/file"},
		{name: "windows absolute", filename: "C:\\file"},
		{name: "UNC", filename: "\\\\server\\file"},
		{name: "NUL", filename: "a\x00b"},
		{name: "invalid UTF8", filename: "a\xff"},
		{name: "too many bytes", filename: strings.Repeat("a", 65536)},
		{name: "UTF8 byte length", filename: strings.Repeat("名", 21846)},
	}
	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			v, err := codec.Encode(nil, tt.filename, layout.Options{})
			if err == nil || v.Colors != nil {
				t.Fatal("invalid filename accepted or allocated colors")
			}
		})
	}
	for _, name := range []string{"CON", "NUL.txt", "COM1", "trailing.", "space ", "a:b", "résumé.pdf", "..hidden"} {
		t.Run("accepted/"+name, func(t *testing.T) {
			v, err := codec.Encode(nil, name, layout.Options{})
			if err != nil {
				t.Fatal(err)
			}
			file, err := codec.Decode(context.Background(), v, 1)
			if err != nil || file.Name != name {
				t.Fatalf("filesystem-specific name = %q, %v", file.Name, err)
			}
		})
	}
}

func TestEncodeResourceBounds(t *testing.T) {
	payload := make([]byte, volume.MaxBlocks/2-23)
	if v, err := codec.Encode(payload, "x", layout.Options{}); err == nil || v.Colors != nil {
		t.Fatal("oversized payload accepted or allocated colors")
	}
	if v, err := codec.Encode(nil, "x", layout.Options{MaxHeight: 400}); err == nil || v.Colors != nil {
		t.Fatal("invalid options accepted or allocated colors")
	}
}

func TestDecodeIndependentUnitOrder(t *testing.T) {
	// The header spans units and every dimension spans multiple units. This
	// fixture is generated by normalized coordinates, independent of Encode.
	name := strings.Repeat("n", 5000) + ".bin"
	payload := patterned(24000)
	v := referenceVolume(48, 32, 64, referenceStream(name, payload))
	for _, workers := range []int{1, 2, 3, 7, 24, 256} {
		t.Run(fmt.Sprintf("workers%d", workers), func(t *testing.T) {
			file, err := codec.Decode(context.Background(), v, workers)
			if err != nil {
				t.Fatal(err)
			}
			if file.Name != name || !bytes.Equal(file.Payload, payload) {
				t.Fatal("independent unit stream decoded in wrong order")
			}
		})
	}
}

func TestDecodeCorruption(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]byte)
		want   string
	}{
		{name: "magic", mutate: func(s []byte) { s[0] ^= 1 }, want: "header"},
		{name: "version", mutate: func(s []byte) { s[8] = 2 }, want: "version"},
		{name: "length exceeds capacity", mutate: func(s []byte) {
			binary.BigEndian.PutUint64(s[9:17], 2048)
		}, want: "payload length"},
		{name: "length overflows host int", mutate: func(s []byte) {
			binary.BigEndian.PutUint64(s[9:17], ^uint64(0))
		}, want: "payload length"},
		{name: "name exceeds capacity", mutate: func(s []byte) {
			binary.BigEndian.PutUint16(s[17:19], 65535)
		}, want: "filename length"},
		{name: "empty name", mutate: func(s []byte) {
			binary.BigEndian.PutUint16(s[17:19], 0)
		}, want: "filename"},
		{name: "dot name", mutate: func(s []byte) { s[23] = '.' }, want: "filename"},
		{name: "slash name", mutate: func(s []byte) { s[23] = '/' }, want: "filename"},
		{name: "backslash name", mutate: func(s []byte) { s[23] = '\\' }, want: "filename"},
		{name: "NUL name", mutate: func(s []byte) { s[23] = 0 }, want: "filename"},
		{name: "UTF8 name", mutate: func(s []byte) { s[23] = 0xff }, want: "UTF-8"},
		{name: "CRC field", mutate: func(s []byte) { s[19] ^= 1 }, want: "checksum"},
		{name: "payload", mutate: func(s []byte) { s[24] ^= 1 }, want: "checksum"},
		{name: "zero payload changes", mutate: func(s []byte) { s[27] = 1 }, want: "checksum"},
		{name: "padding", mutate: func(s []byte) { s[28] = 1 }, want: "padding"},
		{name: "last padding low nibble", mutate: func(s []byte) { s[len(s)-1] = 1 }, want: "padding"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stream := make([]byte, 2048)
			copy(stream, referenceStream("x", []byte{0xae, 0xff, 0, 0}))
			tt.mutate(stream)
			v := referenceVolume(16, 16, 16, stream)
			for _, workers := range []int{1, 256} {
				file, err := codec.Decode(context.Background(), v, workers)
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("workers %d: got %v, want %q", workers, err, tt.want)
				}
				if file.Name != "" || file.Payload != nil {
					t.Fatal("partial file returned on failure")
				}
			}
		})
	}
}

func TestDecodeColorFailuresDeterministic(t *testing.T) {
	stream := referenceStream("x", patterned(10000))
	v := referenceVolume(32, 32, 32, stream)
	// Inject corrupt colors in several units, including a low nibble and
	// padding. Error selection must use logical ordering, not native order.
	for _, nibble := range []int{4096 + 1, 3 * 4096, 7*4096 + 17} {
		setNibble(&v, nibble, 16)
	}
	for _, workers := range []int{1, 2, 3, 4, 7, 256} {
		t.Run(fmt.Sprintf("workers%d", workers), func(t *testing.T) {
			for range 5 {
				_, err := codec.Decode(context.Background(), v, workers)
				if err == nil || !strings.Contains(err.Error(), "logical nibble 4097 ") {
					t.Fatalf("wrong deterministic corruption diagnostic: %v", err)
				}
			}
		})
	}
	padding := referenceVolume(16, 16, 16, referenceStream("x", nil))
	setNibble(&padding, 4095, 255)
	if _, err := codec.Decode(context.Background(), padding, 1); err == nil {
		t.Fatal("invalid color in padding accepted")
	}
}

func TestDecodeResourceBounds(t *testing.T) {
	tests := []struct {
		name    string
		v       volume.Volume
		workers int
	}{
		{name: "zero dimensions", workers: 1},
		{name: "partial unit", v: volume.Volume{X: 17, Y: 16, Z: 16}, workers: 1},
		{name: "negative dimension", v: volume.Volume{X: -16, Y: 16, Z: 16}, workers: 1},
		{name: "oversized volume", v: volume.Volume{X: volume.MaxBlocks, Y: 16, Z: 16}, workers: 1},
		{name: "overflow dimension", v: volume.Volume{X: int(^uint(0) >> 1), Y: 16, Z: 16}, workers: 1},
		{name: "missing colors", v: volume.Volume{X: 16, Y: 16, Z: 16}, workers: 1},
		{name: "short colors", v: volume.Volume{X: 16, Y: 16, Z: 16, Colors: make([]byte, 4095)}, workers: 1},
		{name: "extra colors", v: volume.Volume{X: 16, Y: 16, Z: 16, Colors: make([]byte, 4097)}, workers: 1},
		{name: "zero workers", workers: 0},
		{name: "negative workers", workers: -1},
		{name: "too many workers", workers: 257},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := codec.Decode(context.Background(), tt.v, tt.workers); err == nil {
				t.Fatal("invalid resource request accepted")
			}
		})
	}
}

func TestDecodeCancellation(t *testing.T) {
	v := referenceVolume(16, 16, 16, referenceStream("x", nil))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := codec.Decode(ctx, v, 256); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation not propagated: %v", err)
	}
	deadline, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	if _, err := codec.Decode(deadline, v, 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline not propagated: %v", err)
	}
	if _, err := codec.Decode(nil, v, 1); err == nil {
		t.Fatal("nil context accepted")
	}
}

func patterned(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*37 + i/2048*19)
	}
	return b
}

func referenceStream(name string, payload []byte) []byte {
	s := make([]byte, 23+len(name)+len(payload))
	copy(s, "SCHEMATA")
	s[8] = 1
	binary.BigEndian.PutUint64(s[9:17], uint64(len(payload)))
	binary.BigEndian.PutUint16(s[17:19], uint16(len(name)))
	binary.BigEndian.PutUint32(s[19:23], crc32.ChecksumIEEE(payload))
	copy(s[23:], name)
	copy(s[23+len(name):], payload)
	return s
}

func assertColors(t *testing.T, got volume.Volume, stream []byte) {
	t.Helper()
	want := referenceVolume(got.X, got.Y, got.Z, stream)
	if len(got.Colors) != len(want.Colors) {
		t.Fatalf("color lengths %d, want %d", len(got.Colors), len(want.Colors))
	}
	if !bytes.Equal(got.Colors, want.Colors) {
		for i := range want.Colors {
			if got.Colors[i] != want.Colors[i] {
				t.Fatalf("native color %d = %d, want %d", i, got.Colors[i], want.Colors[i])
			}
		}
	}
}

func referenceVolume(x, y, z int, stream []byte) volume.Volume {
	v := volume.Volume{X: x, Y: y, Z: z, Colors: make([]byte, x*y*z)}
	// Visit native coordinates and compute the logical offset from the spec.
	// This deliberately differs from unit-by-unit production traversal.
	for yy := range y {
		for zz := range z {
			for xx := range x {
				unit := ((yy/16)*(x/16)+xx/16)*(z/16) + zz/16
				local := ((yy%16)*16+xx%16)*16 + zz%16
				nibble := unit*4096 + local
				if nibble/2 >= len(stream) {
					continue
				}
				b := stream[nibble/2]
				color := b & 15
				if nibble%2 == 0 {
					color = b >> 4
				}
				v.Colors[(yy*z+zz)*x+xx] = color
			}
		}
	}
	return v
}

func setNibble(v *volume.Volume, nibble int, color byte) {
	unit, local := nibble/4096, nibble%4096
	ux, uz := v.X/16, v.Z/16
	x := (unit/uz%ux)*16 + (local/16)%16
	y := (unit/(ux*uz))*16 + local/256
	z := (unit%uz)*16 + local%16
	v.Colors[(y*v.Z+z)*v.X+x] = color
}
