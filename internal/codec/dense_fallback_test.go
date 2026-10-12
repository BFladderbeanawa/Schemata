//go:build integration

package codec

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"math/big"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/bfladderbeanawa/schemata/internal/alphabet"
	"github.com/bfladderbeanawa/schemata/internal/schematic"
	"github.com/bfladderbeanawa/schemata/internal/volume"
)

func TestDenseFallbackRecoversWithoutExceedingNBTBudget(t *testing.T) {
	t.Parallel()
	payload := bytes.Repeat([]byte{255, 17, 0, 173, 91}, 20000)
	v, err := EncodeDense(payload, "data.bin", DenseOptions{Banners: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Banners) == 0 {
		t.Fatal("full-alphabet data disappeared")
	}
	rankAt := func(position int) *big.Int {
		rank, err := alphabet.Rank(v.Palette[v.Blocks[position]], v.Banners[position])
		if err != nil {
			t.Fatal(err)
		}
		return rank
	}
	plan, _, err := scanDenseChunks(len(payload)+len("data.bin"), len(v.Blocks), denseVersion, rankAt)
	if err != nil {
		t.Fatal(err)
	}
	transition := -1
	for i, chunk := range plan {
		if chunk.fallback && transition < 0 {
			transition = i
		}
		if transition >= 0 && !chunk.fallback {
			t.Fatal("writer resumed banner spending after fallback")
		}
	}
	if transition <= 0 {
		t.Fatalf("missing full-to-fallback transition: %d", transition)
	}
	var encoded bytes.Buffer
	if err := schematic.Write(&encoded, ".litematic", v); err != nil {
		t.Fatalf("fallback exceeds serializer budgets: %v", err)
	}
	parsed, err := schematic.Read(bytes.NewReader(encoded.Bytes()), ".litematic")
	if err != nil {
		t.Fatal(err)
	}
	for _, workers := range []int{1, 4, 256} {
		file, err := Decode(context.Background(), parsed, workers)
		if err != nil || file.Name != "data.bin" || !bytes.Equal(file.Payload, payload) {
			t.Fatalf("workers %d: %v", workers, err)
		}
	}
	// A marker is data, not an ignorable palette convention.
	marker := plan[transition].position - 1
	v.Palette = append(v.Palette, alphabet.States()[2])
	v.Blocks[marker] = uint32(len(v.Palette) - 1)
	if _, err := Decode(context.Background(), v, 4); err == nil || !strings.Contains(err.Error(), "marker") {
		t.Fatalf("invalid marker accepted: %v", err)
	}
}

func TestDenseVersion2StillRecovers(t *testing.T) {
	t.Parallel()
	name := "legacy-v2.bin"
	payload := []byte{0, 0, 255, 173, 0, 0}
	var header [prefixBytes]byte
	copy(header[:8], magic)
	header[8] = 2
	binary.BigEndian.PutUint64(header[9:17], uint64(len(payload)))
	binary.BigEndian.PutUint16(header[17:19], uint16(len(name)))
	binary.BigEndian.PutUint32(header[19:23], crc32.ChecksumIEEE(payload))
	states := make([]volume.State, 0)
	blocks := make([]uint32, 0)
	banners := make(map[int][]volume.Layer)
	put := func(state volume.State, layers []volume.Layer) {
		position := len(blocks)
		blocks = append(blocks, uint32(len(states)))
		states = append(states, state)
		if alphabet.IsBanner(state) {
			banners[position] = layers
		}
	}
	for _, b := range header {
		for _, nibble := range []byte{b >> 4, b & 15} {
			put(volume.State{Name: "minecraft:" + concreteNames[nibble] + "_concrete"}, nil)
		}
	}
	stream := append([]byte(name), payload...)
	for _, rank := range bytesToSymbols(stream, denseGeometry()) {
		state, layers, err := alphabet.Unrank(rank)
		if err != nil {
			t.Fatal(err)
		}
		put(state, layers)
	}
	v := volume.Volume{X: len(blocks), Y: 1, Z: 1, Palette: states, Blocks: blocks, Banners: banners}
	var encoded bytes.Buffer
	if err := schematic.Write(&encoded, ".litematic", v); err != nil {
		t.Fatal(err)
	}
	parsed, err := schematic.Read(bytes.NewReader(encoded.Bytes()), ".litematic")
	if err != nil {
		t.Fatal(err)
	}
	for _, workers := range []int{1, 4} {
		file, err := Decode(context.Background(), parsed, workers)
		if err != nil || file.Name != name || !bytes.Equal(file.Payload, payload) || file.Part != nil {
			t.Fatalf("version2 workers%d: %v", workers, err)
		}
	}
}

func TestDenseFallbackDeterministicAcrossScheduling(t *testing.T) {
	old := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(old)
	payload := bytes.Repeat([]byte{0, 173, 255, 31}, 25000)
	a, err := EncodeDense(payload, "data.bin", DenseOptions{Banners: true})
	if err != nil {
		t.Fatal(err)
	}
	runtime.GOMAXPROCS(4)
	b, err := EncodeDense(payload, "data.bin", DenseOptions{Banners: true})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatal("fallback structure depends on scheduling")
	}
}

func TestDensePartSchematicRoundTrip(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		payload []byte
		banners bool
		part    *PartInfo
	}{
		{name: "empty", payload: []byte{}},
		{name: "zero boundaries", payload: bytes.Repeat([]byte{0, 0, 255, 173, 0}, 137)},
		{name: "multipart final", payload: []byte{173, 0}, part: &PartInfo{TotalLength: 5, Offset: 3, Index: 1, Count: 2, PartSize: 3}},
		{name: "banner fallback", payload: bytes.Repeat([]byte{255, 17, 0, 173, 91}, 20000), banners: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			part := singleDensePart(tc.payload)
			if tc.part != nil {
				part = *tc.part
			}
			v, err := EncodeDensePart(tc.payload, "original.zip", DenseOptions{Banners: tc.banners}, part)
			if err != nil {
				t.Fatal(err)
			}
			if tc.banners {
				rankAt := func(position int) *big.Int {
					rank, err := alphabet.Rank(v.Palette[v.Blocks[position]], v.Banners[position])
					if err != nil {
						t.Fatal(err)
					}
					return rank
				}
				plan, _, err := scanDenseChunks(
					len("original.zip")+partManifestBytes+len(tc.payload),
					len(v.Blocks),
					densePartVersion,
					rankAt,
				)
				if err != nil {
					t.Fatal(err)
				}
				if len(v.Banners) == 0 || !plan[len(plan)-1].fallback {
					t.Fatal("multipart fixture did not exercise full-to-fallback transition")
				}
			}
			var encoded bytes.Buffer
			if err := schematic.Write(&encoded, ".litematic", v); err != nil {
				t.Fatal(err)
			}
			parsed, err := schematic.Read(bytes.NewReader(encoded.Bytes()), ".litematic")
			if err != nil {
				t.Fatal(err)
			}
			for _, workers := range []int{1, 4} {
				file, err := Decode(context.Background(), parsed, workers)
				if err != nil {
					t.Fatal(err)
				}
				if file.Name != "original.zip" || !bytes.Equal(file.Payload, tc.payload) || file.Part == nil || *file.Part != part {
					t.Fatalf("workers %d: serializer round-trip differs", workers)
				}
			}
		})
	}
}
