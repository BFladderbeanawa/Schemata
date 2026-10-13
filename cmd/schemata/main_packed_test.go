//go:build integration

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
)

func writeInput(t *testing.T, path string, payload []byte) {
	t.Helper()
	if err := os.WriteFile(path, payload, 0600); err != nil {
		t.Fatal(err)
	}
}

func assertRecovered(t *testing.T, path string, payload []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("recovery %s: %v", path, err)
	}
}

func TestPackedCLISingleVolumeRecovery(t *testing.T) {
	t.Parallel()
	random := make([]byte, 70000)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		payload []byte
		flags   []string
	}{
		{name: "default compressible", payload: append(bytes.Repeat([]byte{0, 255, 174, 39}, 20000), 0, 0)},
		{name: "explicit incompressible", payload: random, flags: []string{"--encoding", "packed"}},
		{name: "empty compact", payload: []byte{}, flags: []string{"--layout", "compact"}},
		{name: "bounded uncapped", payload: []byte("x"), flags: []string{"--layout", "bounded", "--max-height", "0"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			input := filepath.Join(dir, "数据.schematic")
			writeInput(t, input, tc.payload)
			structure := filepath.Join(dir, "archive.litematic")
			args := append(append([]string{"encode"}, tc.flags...), input, structure)
			execute(t, args, 0)
			execute(t, args, 0)
			if _, err := os.Stat(filepath.Join(dir, "archive (1).litematic")); err != nil {
				t.Fatal(err)
			}
			v, err := readStructure(structure, ".litematic")
			if err != nil || v.Packed == nil {
				t.Fatalf("not a packed structure: %v", err)
			}
			dest := filepath.Join(dir, "out")
			for _, workers := range []string{"1", "4"} {
				execute(t, []string{"decode", "--workers", workers, structure, dest}, 0)
			}
			assertRecovered(t, filepath.Join(dest, "数据.schematic"), tc.payload)
			assertRecovered(t, filepath.Join(dest, "数据 (1).schematic"), tc.payload)
			if entries, _ := os.ReadDir(dest); len(entries) != 2 {
				t.Fatalf("destination has %d entries; temporary files leaked", len(entries))
			}
		})
	}
}

func TestPackedCLIMultipartWorkersAndRenames(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	input := filepath.Join(dir, "large.bin")
	payload := make([]byte, 2*1024*1024+12345)
	if _, err := rand.Read(payload[:1024*1024]); err != nil {
		t.Fatal(err)
	}
	writeInput(t, input, payload)
	var structures [2][][]byte
	for i, workers := range []string{"1", "4"} {
		requested := filepath.Join(dir, "w"+workers+".litematic")
		execute(t, []string{"encode", "--workers", workers, "--part-size-mib", "1", input, requested}, 0)
		parts := filepath.Join(dir, "w"+workers+".parts")
		for index := 1; index <= 3; index++ {
			v, err := readStructure(filepath.Join(parts, []string{"", "000001", "000002", "000003"}[index]+".litematic"), ".litematic")
			if err != nil || v.Packed == nil {
				t.Fatalf("part %d: %v", index, err)
			}
			structures[i] = append(structures[i], v.Packed)
		}
	}
	for index := range structures[0] {
		if !bytes.Equal(structures[0][index], structures[1][index]) {
			t.Fatalf("part %d logical structure depends on workers", index+1)
		}
	}
	parts := filepath.Join(dir, "w4.parts")
	if err := os.Rename(filepath.Join(parts, "000001.litematic"), filepath.Join(parts, "z.litematic")); err != nil {
		t.Fatal(err)
	}
	for _, workers := range []string{"1", "4"} {
		execute(t, []string{"decode", "--workers", workers, parts, filepath.Join(dir, "out")}, 0)
	}
	assertRecovered(t, filepath.Join(dir, "out", "large.bin"), payload)
	assertRecovered(t, filepath.Join(dir, "out", "large (1).bin"), payload)
	execute(t, []string{"decode", "--filename", "r.bin", filepath.Join(parts, "000002.litematic"), filepath.Join(dir, "r")}, 0)
	assertRecovered(t, filepath.Join(dir, "r", "r.bin"), payload)
}

func TestPackedCLIRejectsBadSets(t *testing.T) {
	t.Parallel()
	payload := bytes.Repeat([]byte{1, 2, 3, 4, 5, 6, 7}, 450000)
	for _, failure := range []string{"missing", "duplicate", "mixed group", "aggregate", "corrupt", "foreign"} {
		t.Run(failure, func(t *testing.T) {
			dir := t.TempDir()
			input := filepath.Join(dir, "data.bin")
			writeInput(t, input, payload)
			execute(t, []string{"encode", "--part-size-mib", "1", input, filepath.Join(dir, "a.litematic")}, 0)
			parts := filepath.Join(dir, "a.parts")
			second := filepath.Join(parts, "000002.litematic")
			otherSecond := func(mutate int) []byte {
				changed := append([]byte(nil), payload...)
				changed[mutate] ^= 0xff
				writeInput(t, input, changed)
				execute(t, []string{"encode", "--part-size-mib", "1", input, filepath.Join(dir, "b.litematic")}, 0)
				data, err := os.ReadFile(filepath.Join(dir, "b.parts", "000002.litematic"))
				if err != nil {
					t.Fatal(err)
				}
				return data
			}
			switch failure {
			case "missing":
				if err := os.Remove(second); err != nil {
					t.Fatal(err)
				}
			case "duplicate":
				data, _ := os.ReadFile(filepath.Join(parts, "000001.litematic"))
				writeInput(t, second, data)
			case "mixed group":
				writeInput(t, second, otherSecond(0))
			case "aggregate":
				// Same part 0 and group; only the aggregate digest detects the mix.
				writeInput(t, second, otherSecond(1024*1024+5))
			case "corrupt":
				writeInput(t, second, []byte("truncated"))
			case "foreign":
				execute(t, []string{"encode", "--encoding", "dense", input, filepath.Join(dir, "dense.litematic")}, 0)
				data, _ := os.ReadFile(filepath.Join(dir, "dense.litematic"))
				writeInput(t, second, data)
			}
			dest := filepath.Join(dir, "out")
			execute(t, []string{"decode", parts, dest}, 1)
			if _, err := os.Stat(dest); !os.IsNotExist(err) {
				t.Fatalf("failed set published destination: %v", err)
			}
		})
	}
}

func TestPackedCLIForcedEmptyAndCancellation(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	input := filepath.Join(dir, "empty.bin")
	writeInput(t, input, nil)
	execute(t, []string{"encode", "--part-size-mib", "79", input, filepath.Join(dir, "e.litematic")}, 0)
	execute(t, []string{"decode", filepath.Join(dir, "e.parts"), filepath.Join(dir, "out")}, 0)
	assertRecovered(t, filepath.Join(dir, "out", "empty.bin"), []byte{})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	if run(ctx, []string{"encode", "--part-size-mib", "1", input, filepath.Join(dir, "c.litematic")}, &stdout, &stderr) != 1 {
		t.Fatal("cancelled encode succeeded")
	}
	if _, err := os.Stat(filepath.Join(dir, "c.parts")); !os.IsNotExist(err) {
		t.Fatalf("cancelled encode left a set: %v", err)
	}
	dest := filepath.Join(dir, "cancelled")
	if run(ctx, []string{"decode", filepath.Join(dir, "e.parts"), dest}, &stdout, &stderr) != 1 {
		t.Fatal("cancelled decode succeeded")
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("cancelled decode published destination: %v", err)
	}
}
