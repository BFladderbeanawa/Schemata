// Schemata stores files in schematic structures and recovers their exact bytes.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/bfladderbeanawa/schemata/internal/codec"
	"github.com/bfladderbeanawa/schemata/internal/layout"
	"github.com/bfladderbeanawa/schemata/internal/output"
	"github.com/bfladderbeanawa/schemata/internal/schematic"
	"github.com/bfladderbeanawa/schemata/internal/volume"
)

// Two commands need no configuration layering; stdlib flag keeps this CLI dependency-free.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: schemata <encode|decode> [flags] arguments; use --help")
		return 2
	}
	if args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		fmt.Fprintln(stdout, "usage: schemata encode [flags] input-file output.litematic|output.schematic")
		fmt.Fprintln(stdout, "       schemata decode [flags] input.litematic|input.schematic destination-directory")
		fmt.Fprintln(stdout, "use schemata encode --help or schemata decode --help for flags")
		return 0
	}
	var err error
	var path string
	var usage bool
	switch args[0] {
	case "encode":
		path, usage, err = encode(ctx, args[1:], stderr)
	case "decode":
		path, usage, err = decode(ctx, args[1:], stderr)
	default:
		fmt.Fprintf(stderr, "unknown command %q; expected encode or decode\n", args[0])
		return 2
	}
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "schemata: %v\n", err)
		if usage {
			return 2
		}
		return 1
	}
	fmt.Fprintln(stdout, path)
	return 0
}

func encode(ctx context.Context, args []string, help io.Writer) (string, bool, error) {
	fs := flag.NewFlagSet("encode", flag.ContinueOnError)
	fs.SetOutput(help)
	height := fs.Int("max-height", layout.DefaultMaxHeight, "height cap in blocks: multiple of 16 from 16 through 384, or 0 for no cap")
	mode := fs.String("encoding", "", "dense (.litematic default) or concrete (.schematic default)")
	banners := fs.Bool("banners", false, "enable banner symbols in dense encoding (disabled by default)")
	policy := fs.String("layout", "", "balanced (default), compact (minimum positions/units), or bounded")
	padding := fs.Int("padding-percent", 25, "bounded layout extra positions/units as percent of required capacity (0–100)")
	partSize := fs.Int("part-size-mib", 16, "multipart payload per volume (1–32 MiB); concrete caps to header/manifest capacity; explicit flag forces multipart")
	fs.Usage = func() {
		fmt.Fprintln(help, "usage: schemata encode [flags] input-file output.litematic|output.schematic")
		fmt.Fprintln(help, "dense defaults to nonbanner states; --banners enables the full alphabet with resource-aware fallback")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return "", true, err
	}
	if fs.NArg() != 2 {
		return "", true, errors.New("encode requires input file and output schematic path")
	}
	modeSet, policySet, partSizeSet := false, false, false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "encoding" {
			modeSet = true
		}
		if f.Name == "layout" {
			policySet = true
		}
		if f.Name == "part-size-mib" {
			partSizeSet = true
		}
	})
	if policySet && *policy == "" {
		return "", true, errors.New("layout must be balanced, compact, or bounded")
	}
	ext, err := formatExtension(fs.Arg(1))
	if err != nil {
		return "", true, err
	}
	if !modeSet {
		*mode = "dense"
		if ext == ".schematic" {
			*mode = "concrete"
		}
	}
	if *mode != "dense" && *mode != "concrete" {
		return "", true, errors.New("encoding must be dense or concrete")
	}
	if *mode == "dense" && ext != ".litematic" {
		return "", true, errors.New("dense encoding requires .litematic output")
	}
	if *banners && *mode != "dense" {
		return "", true, errors.New("banners require dense encoding")
	}
	if *partSize < 1 || *partSize > 32 {
		return "", true, errors.New("part size must be from 1 through 32 MiB")
	}
	opts := layout.Options{MaxHeight: *height, NoHeightLimit: *height == 0, Policy: *policy, PaddingPercent: *padding}
	selectLayout := layout.Select
	if *mode == "dense" {
		selectLayout = layout.SelectDense
	}
	if _, err := selectLayout(1, opts); err != nil {
		return "", true, err
	}
	info, err := os.Stat(fs.Arg(0))
	if err != nil {
		return "", false, fmt.Errorf("stat input: %w", err)
	}
	if needsMultipart(info.Size(), filepath.Base(fs.Arg(0)), *mode, partSizeSet) {
		path, err := encodeParts(
			ctx,
			fs.Arg(0),
			fs.Arg(1),
			partsOptions{
				Encoding:  *mode,
				Extension: ext,
				Layout:    opts,
				Banners:   *banners,
				PartSize:  *partSize * 1024 * 1024,
			},
		)
		return path, false, err
	}
	payload, err := readPayload(fs.Arg(0))
	if err != nil {
		return "", false, err
	}
	var v volume.Volume
	if *mode == "dense" {
		v, err = codec.EncodeDense(payload, filepath.Base(fs.Arg(0)), codec.DenseOptions{Layout: opts, Banners: *banners})
	} else {
		v, err = codec.Encode(payload, filepath.Base(fs.Arg(0)), opts)
	}
	if err != nil {
		return "", false, err
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	path, err := output.Create(filepath.Dir(fs.Arg(1)), filepath.Base(fs.Arg(1)), func(w io.Writer) error {
		return schematic.Write(w, ext, v)
	})
	return path, false, err
}

func decode(ctx context.Context, args []string, help io.Writer) (string, bool, error) {
	fs := flag.NewFlagSet("decode", flag.ContinueOnError)
	fs.SetOutput(help)
	name := fs.String("filename", "", "replacement output basename (never a path)")
	workers := fs.Int("workers", min(runtime.GOMAXPROCS(0), 256), "concurrent chunk/unit decoding workers (1–256)")
	fs.Usage = func() {
		fmt.Fprintln(help, "usage: schemata decode [flags] input.litematic|input.schematic destination-directory")
		fmt.Fprintln(help, "multipart: supply a .parts directory or any part file; all parts must be together")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return "", true, err
	}
	if fs.NArg() != 2 {
		return "", true, errors.New("decode requires input schematic and destination directory")
	}
	if *workers < 1 || *workers > 256 {
		return "", true, errors.New("workers must be from 1 through 256")
	}
	nameSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "filename" {
			nameSet = true
		}
	})
	if nameSet {
		if err := output.ValidateName(*name); err != nil {
			return "", true, err
		}
	}
	info, err := os.Stat(fs.Arg(0))
	if err != nil {
		return "", false, fmt.Errorf("stat input: %w", err)
	}
	if info.IsDir() {
		path, err := decodeParts(ctx, fs.Arg(0), fs.Arg(1), *name, *workers)
		return path, false, err
	}
	ext, err := formatExtension(fs.Arg(0))
	if err != nil {
		return "", true, err
	}
	v, err := readStructure(fs.Arg(0), ext)
	if err != nil {
		return "", false, err
	}
	file, err := codec.Decode(ctx, v, *workers)
	if err != nil {
		return "", false, err
	}
	if file.Part != nil {
		path, err := decodeParts(ctx, fs.Arg(0), fs.Arg(1), *name, *workers)
		return path, false, err
	}
	filename := file.Name
	if nameSet {
		filename = *name
	}
	if err := output.ValidateName(filename); err != nil {
		return "", false, err
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if err := os.MkdirAll(fs.Arg(1), 0755); err != nil {
		return "", false, fmt.Errorf("create destination directory: %w", err)
	}
	path, err := output.Create(fs.Arg(1), filename, func(w io.Writer) error {
		_, err := io.Copy(w, bytes.NewReader(file.Payload))
		return err
	})
	return path, false, err
}

func formatExtension(path string) (string, error) {
	ext := strings.ToLower(filepath.Ext(path))
	if ext != ".litematic" && ext != ".schematic" {
		return "", fmt.Errorf("unsupported schematic extension %q", ext)
	}
	return ext, nil
}

func readPayload(path string) (payload []byte, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open input: %w", err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat input: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("payload must be a regular file")
	}
	const limit = volume.MaxBlocks / 2
	if info.Size() > limit {
		return nil, fmt.Errorf("payload exceeds %d byte resource limit", limit)
	}
	payload, err = io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read payload: %w", err)
	}
	if len(payload) > limit {
		return nil, errors.New("payload grew beyond resource limit while reading")
	}
	return payload, nil
}

func readStructure(path, ext string) (v volume.Volume, err error) {
	f, err := os.Open(path)
	if err != nil {
		return v, fmt.Errorf("open schematic: %w", err)
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	v, err = schematic.Read(f, ext)
	return v, err
}
