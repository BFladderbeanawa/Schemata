//go:build integration

package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/bfladderbeanawa/schemata/internal/codec"
	"github.com/bfladderbeanawa/schemata/internal/layout"
	"github.com/bfladderbeanawa/schemata/internal/schematic"
)

func TestCLIByteExactRecovery(t *testing.T) {
	t.Parallel()
	for _, ext := range []string{".litematic", ".schematic"} {
		t.Run(ext, func(t *testing.T) {
			dir := t.TempDir()
			payload := append(bytes.Repeat([]byte{0, 255, 174, 39}, 2100), 0, 0, 0)
			input := filepath.Join(dir, "data.schematic")
			if err := os.WriteFile(input, payload, 0600); err != nil {
				t.Fatal(err)
			}
			structure := filepath.Join(dir, "archive"+ext)
			execute(t, []string{"encode", "--max-height", "16", "--layout", "compact", input, structure}, 0)
			execute(t, []string{"encode", input, structure}, 0)
			if _, err := os.Stat(filepath.Join(dir, "archive (1)"+ext)); err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(dir, "recovered")
			execute(t, []string{"decode", "--workers", "1", structure, dest}, 0)
			execute(t, []string{"decode", "--workers", "4", structure, dest}, 0)
			for _, name := range []string{"data.schematic", "data (1).schematic"} {
				data, err := os.ReadFile(filepath.Join(dest, name))
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(data, payload) {
					t.Fatalf("recovered %q differs", name)
				}
			}
		})
	}
}

func TestCLICorruptionPublishesNothing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	v, err := codec.Encode([]byte("verified bytes"), "payload.bin", layout.Options{MaxHeight: 64, Policy: "balanced", PaddingPercent: 25})
	if err != nil {
		t.Fatal(err)
	}
	// Header prefix and filename occupy 34 bytes; alter the first payload nibble.
	v.Colors[v.Index(4, 0, 4)] ^= 1
	path := filepath.Join(dir, "corrupt.litematic")
	writeStructure(t, path, func(w io.Writer) error { return schematic.Write(w, ".litematic", v) })
	dest := filepath.Join(dir, "destination")
	execute(t, []string{"decode", path, dest}, 1)
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("corrupt recovery created destination: %v", err)
	}
}

func TestCLIUnsafeStoredNameReplacement(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows filesystem name policy")
	}
	t.Parallel()
	dir := t.TempDir()
	payload := []byte("reserved-name payload")
	v, err := codec.Encode(payload, "CON.txt", layout.Options{MaxHeight: 64, Policy: "balanced", PaddingPercent: 25})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "reserved.schematic")
	writeStructure(t, path, func(w io.Writer) error { return schematic.Write(w, ".schematic", v) })
	dest := filepath.Join(dir, "destination")
	execute(t, []string{"decode", path, dest}, 1)
	execute(t, []string{"decode", "--filename", "../escape.txt", path, dest}, 2)
	execute(t, []string{"decode", "--filename", "safe.txt", path, dest}, 0)
	got, err := os.ReadFile(filepath.Join(dest, "safe.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("replacement recovery differs: %q", got)
	}
}

func writeStructure(t *testing.T, path string, write func(io.Writer) error) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writeErr := write(f)
	closeErr := f.Close()
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
}

func execute(t *testing.T, args []string, want int) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	got := run(context.Background(), args, &stdout, &stderr)
	if got != want {
		t.Fatalf("%v: exit %d, want %d; stdout=%q stderr=%q", args, got, want, stdout.String(), stderr.String())
	}
}
