package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/bfladderbeanawa/schemata/internal/codec"
)

func TestInvalidFlagsDoNotPublishOutput(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "subunit height", args: []string{"encode", "--max-height", "8", "missing.bin", "out.litematic"}},
		{name: "nonunit height", args: []string{"encode", "--max-height", "17", "missing.bin", "out.litematic"}},
		{name: "overworld height", args: []string{"encode", "--max-height", "400", "missing.bin", "out.schematic"}},
		{name: "empty layout", args: []string{"encode", "--layout", "", "missing.bin", "out.litematic"}},
		{name: "padding overflow", args: []string{"encode", "--layout", "bounded", "--padding-percent", "101", "missing.bin", "out.litematic"}},
		{name: "unsupported output", args: []string{"encode", "missing.bin", "out.schem"}},
		{name: "dense legacy output", args: []string{"encode", "--encoding", "dense", "missing.bin", "out.schematic"}},
		{name: "banners with concrete", args: []string{"encode", "--encoding", "concrete", "--banners", "missing.bin", "out.litematic"}},
		{name: "banners with legacy output", args: []string{"encode", "--banners", "missing.bin", "out.schematic"}},
		{name: "zero part size", args: []string{"encode", "--part-size-mib", "0", "missing.bin", "out.litematic"}},
		{name: "excess part size", args: []string{"encode", "--part-size-mib", "33", "missing.bin", "out.litematic"}},
		{name: "unknown encoding", args: []string{"encode", "--encoding", "other", "missing.bin", "out.litematic"}},
		{name: "empty encoding", args: []string{"encode", "--encoding", "", "missing.bin", "out.litematic"}},
		{name: "no workers", args: []string{"decode", "--workers", "0", "missing.litematic", "out"}},
		{name: "excess workers", args: []string{"decode", "--workers", "257", "missing.litematic", "out"}},
		{name: "invalid replacement", args: []string{"decode", "--filename", "../outside.bin", "missing.litematic", "out"}},
		{name: "unknown flag", args: []string{"encode", "--unknown"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if got := run(context.Background(), tc.args, &stdout, &stderr); got != 2 {
				t.Fatalf("exit %d, want usage error; stderr=%q", got, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("error leaked to stdout: %q", stdout.String())
			}
		})
	}
}

func TestMultipartSelection(t *testing.T) {
	t.Parallel()
	name := "数据.bin"
	concreteCapacity := int64(32*1024*1024 - 23 - len(name))
	for _, tc := range []struct {
		name     string
		encoding string
		length   int64
		explicit bool
		want     bool
	}{
		{name: "concrete capacity", encoding: "concrete", length: concreteCapacity},
		{name: "concrete automatic", encoding: "concrete", length: concreteCapacity + 1, want: true},
		{name: "concrete forced empty", encoding: "concrete", explicit: true, want: true},
		{name: "dense capacity", encoding: "dense", length: codec.MaxDensePayload},
		{name: "dense automatic", encoding: "dense", length: codec.MaxDensePayload + 1, want: true},
		{name: "dense forced empty", encoding: "dense", explicit: true, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := needsMultipart(tc.length, name, tc.encoding, tc.explicit); got != tc.want {
				t.Fatalf("multipart selection = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMultipartPayloadCap(t *testing.T) {
	t.Parallel()
	const mib = 1024 * 1024
	for _, tc := range []struct {
		name      string
		filename  string
		encoding  string
		requested int
		want      int
	}{
		{name: "concrete maximum", filename: "data.bin", encoding: "concrete", requested: 32 * mib,
			want: 32*mib - 23 - len("data.bin") - 64},
		{name: "concrete UTF8 maximum", filename: "数据.bin", encoding: "concrete", requested: 32 * mib,
			want: 32*mib - 23 - len("数据.bin") - 64},
		{name: "concrete below capacity", filename: "data.bin", encoding: "concrete", requested: mib, want: mib},
		{name: "dense maximum unchanged", filename: "data.bin", encoding: "dense", requested: 32 * mib, want: 32 * mib},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := partPayloadSize(tc.filename, tc.encoding, tc.requested); got != tc.want {
				t.Fatalf("part payload size = %d, want %d", got, tc.want)
			}
		})
	}
}
