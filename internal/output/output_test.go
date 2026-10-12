package output

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func TestValidateName(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"", ".", "..", "../escape", `..\escape`, "/absolute", "nul\x00.txt", string([]byte{255})} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateName(name); err == nil {
				t.Fatalf("accepted unsafe name %q", name)
			}
		})
	}
	if runtime.GOOS == "windows" {
		for _, name := range []string{"CON.txt", "NUL", "COM1.bin", "LPT².txt", "trailing.", "trailing ", "a:b", "wild*card", "CONOUT$"} {
			t.Run(name, func(t *testing.T) {
				if err := ValidateName(name); err == nil {
					t.Fatalf("accepted unsafe Windows name %q", name)
				}
			})
		}
	}
	if err := ValidateName("résumé.tar.gz"); err != nil {
		t.Fatal(err)
	}
}

func TestCreatePreservesCollisions(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "report.tar.gz"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	const count = 16
	paths := make([]string, count)
	errs := make([]error, count)
	var wg sync.WaitGroup
	for i := range count {
		wg.Add(1)
		go func() {
			defer wg.Done()
			paths[i], errs[i] = Create(dir, "report.tar.gz", func(w io.Writer) error {
				_, err := w.Write([]byte{byte(i)})
				return err
			})
		}()
	}
	wg.Wait()
	seen := make(map[string]bool)
	for i, path := range paths {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if seen[path] {
			t.Fatalf("duplicate output path %q", path)
		}
		seen[path] = true
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) != 1 || data[0] != byte(i) {
			t.Fatalf("output %q was overwritten: %v", path, data)
		}
	}
	original, err := os.ReadFile(filepath.Join(dir, "report.tar.gz"))
	if err != nil {
		t.Fatal(err)
	}
	if string(original) != "original" {
		t.Fatalf("existing file changed: %q", original)
	}
	if !seen[filepath.Join(dir, "report.tar (1).gz")] {
		t.Fatal("suffix was not placed before final extension")
	}
}

func TestCreateRemovesFailedWrite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	failure := errors.New("simulated write failure")
	_, err := Create(dir, "failed.bin", func(w io.Writer) error {
		if _, err := w.Write([]byte("partial")); err != nil {
			return err
		}
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "failed.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial output remains: %v", err)
	}
}
