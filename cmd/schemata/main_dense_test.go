//go:build integration

package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/bfladderbeanawa/schemata/internal/alphabet"
	"github.com/bfladderbeanawa/schemata/internal/codec"
	"github.com/bfladderbeanawa/schemata/internal/layout"
	"github.com/bfladderbeanawa/schemata/internal/schematic"
)

func TestDenseCLIBannerOptIn(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		flags   []string
		banners bool
	}{
		{name: "default"},
		{name: "explicit dense", flags: []string{"--encoding", "dense"}},
		{name: "disabled", flags: []string{"--banners=false"}},
		{name: "enabled", flags: []string{"--banners"}, banners: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			input := filepath.Join(dir, "data.bin")
			payload := bytes.Repeat([]byte{0, 255, 173, 31, 0}, 205)
			if err := os.WriteFile(input, payload, 0600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "data.litematic")
			args := append([]string{"encode"}, tc.flags...)
			execute(t, append(args, input, path), 0)
			v, err := readStructure(path, ".litematic")
			if err != nil {
				t.Fatal(err)
			}
			if v.X != v.Z || v.Y != min(v.X, 64) {
				t.Fatalf("default layout is not a balanced cuboid: (%d,%d,%d)", v.X, v.Y, v.Z)
			}
			if (len(v.Banners) > 0) != tc.banners {
				t.Fatalf("banner entities = %d, enabled = %v", len(v.Banners), tc.banners)
			}
			if !tc.banners {
				for _, state := range v.Palette {
					if alphabet.IsBanner(state) {
						t.Fatalf("disabled banner state: %s", state.Name)
					}
				}
			}
			for _, workers := range []string{"1", "4"} {
				dest := filepath.Join(dir, "out"+workers)
				execute(t, []string{"decode", "--workers", workers, path, dest}, 0)
				got, err := os.ReadFile(filepath.Join(dest, "data.bin"))
				if err != nil || !bytes.Equal(got, payload) {
					t.Fatalf("workers %s: byte-exact recovery: %v", workers, err)
				}
			}
		})
	}
}

func TestDenseCLIEmptyAndLayoutRecovery(t *testing.T) {
	t.Parallel()
	for _, policy := range []string{"compact", "balanced", "bounded"} {
		t.Run(policy, func(t *testing.T) {
			dir := t.TempDir()
			input := filepath.Join(dir, "空.bin")
			if err := os.WriteFile(input, []byte{}, 0600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "empty.litematic")
			execute(t, []string{"encode", "--encoding", "dense", "--layout", policy, "--max-height", "16", input, path}, 0)
			execute(t, []string{"decode", "--workers", "4", path, filepath.Join(dir, "out")}, 0)
			got, err := os.ReadFile(filepath.Join(dir, "out", "空.bin"))
			if err != nil || len(got) != 0 {
				t.Fatalf("empty recovery: %x, %v", got, err)
			}
		})
	}
}

func TestDenseCLICorruptionPublishesNothing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	v, err := codec.EncodeDense(bytes.Repeat([]byte{1, 2, 0, 255}, 100), "data.bin", codec.DenseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Header CRC byte changed while preserving a structurally valid native state.
	v.Blocks[38] = v.Blocks[0]
	path := filepath.Join(dir, "corrupt.litematic")
	writeStructure(t, path, func(w io.Writer) error { return schematic.Write(w, ".litematic", v) })
	dest := filepath.Join(dir, "out")
	execute(t, []string{"decode", path, dest}, 1)
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("corruption published destination: %v", err)
	}
}

func TestDenseLogicalStructureIndependentOfScheduling(t *testing.T) {
	// Changes the process-wide scheduler setting; deliberately not parallel.
	old := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(old)
	payload := bytes.Repeat([]byte{0, 255, 17, 0, 173}, 409)
	opts := codec.DenseOptions{Layout: layout.Options{Policy: "bounded", PaddingPercent: 25}}
	a, err := codec.EncodeDense(payload, "data.bin", opts)
	if err != nil {
		t.Fatal(err)
	}
	runtime.GOMAXPROCS(4)
	b, err := codec.EncodeDense(payload, "data.bin", opts)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatal("logical structure depends on scheduling")
	}
	var file bytes.Buffer
	if err := schematic.Write(&file, ".litematic", a); err != nil {
		t.Fatal(err)
	}
	v, err := schematic.Read(bytes.NewReader(file.Bytes()), ".litematic")
	if err != nil {
		t.Fatal(err)
	}
	for _, workers := range []int{1, 4} {
		got, err := codec.Decode(context.Background(), v, workers)
		if err != nil || !bytes.Equal(got.Payload, payload) {
			t.Fatalf("workers %d: recovery: %v", workers, err)
		}
	}
}
