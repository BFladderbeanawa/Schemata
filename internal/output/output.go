// Package output validates destination basenames and creates files without overwriting.
package output

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"unicode/utf8"
)

// ValidateName rejects paths everywhere and platform-specific unsafe names.
// Replacement names go through the same validation as stored names.
func ValidateName(name string) error {
	invalidBase := name == "" || name == "." || name == ".."
	invalidPath := strings.ContainsAny(name, "/\\\x00") || filepath.IsAbs(name)
	if invalidBase || invalidPath || !utf8.ValidString(name) {
		return fmt.Errorf("invalid output basename %q", name)
	}
	if runtime.GOOS != "windows" {
		return nil
	}
	if strings.ContainsAny(name, `<>:"|?*`) || strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return fmt.Errorf("filename %q is unsafe on Windows; supply a valid --filename", name)
	}
	for _, c := range name {
		if c < 32 {
			return fmt.Errorf("filename contains a control character")
		}
	}
	base, _, _ := strings.Cut(name, ".")
	base = strings.ToUpper(strings.TrimRight(base, " "))
	reserved := base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || base == "CONIN$" || base == "CONOUT$"
	deviceNumber := strings.ContainsRune("123456789¹²³", lastRune(base))
	device := (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && len([]rune(base)) == 4 && deviceNumber
	if reserved || device {
		return fmt.Errorf("filename %q is reserved on Windows; supply a valid --filename", name)
	}
	return nil
}

func lastRune(s string) rune {
	r, _ := utf8.DecodeLastRuneInString(s)
	return r
}

// Create reserves a collision-free name inside an opened directory root.
// The writer must receive only prevalidated data. Failed writes are removed;
// exclusive creation remains safe against concurrent creators and symlinks.
func Create(dir, name string, write func(io.Writer) error) (path string, err error) {
	if err := ValidateName(name); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", fmt.Errorf("open output directory: %w", err)
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for suffix := 0; ; suffix++ {
		candidate := name
		if suffix > 0 {
			candidate = stem + " (" + strconv.Itoa(suffix) + ")" + ext
		}
		if err := ValidateName(candidate); err != nil {
			return "", err
		}
		f, openErr := root.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if errors.Is(openErr, os.ErrExist) {
			continue
		}
		if openErr != nil {
			return "", fmt.Errorf("create output %q: %w", candidate, openErr)
		}
		writeErr := write(f)
		if writeErr == nil {
			writeErr = f.Sync()
		}
		writeErr = errors.Join(writeErr, f.Close())
		if writeErr != nil {
			removeErr := root.Remove(candidate)
			return "", fmt.Errorf("write output %q: %w", candidate, errors.Join(writeErr, removeErr))
		}
		return filepath.Join(dir, candidate), nil
	}
}
