package main

import (
	"bytes"
	"context"
	"testing"
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
		{name: "concrete multipart", args: []string{"encode", "--encoding", "concrete", "--part-size-mib", "1", "missing.bin", "out.litematic"}},
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
