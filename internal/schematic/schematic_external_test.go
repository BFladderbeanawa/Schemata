//go:build integration

package schematic_test

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/bfladderbeanawa/schemata/internal/codec"
	"github.com/bfladderbeanawa/schemata/internal/schematic"
)

func TestExternalFixturesByteExactRecovery(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, path, ext string }{
		{name: "litemapy", path: "testdata/litemapy-concrete.litematic", ext: ".litematic"},
		{name: "nbtlib legacy", path: "testdata/nbtlib-concrete.schematic", ext: ".schematic"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := os.ReadFile(tc.path)
			if err != nil {
				t.Fatal(err)
			}
			v, err := schematic.Read(bytes.NewReader(data), tc.ext)
			if err != nil {
				t.Fatal(err)
			}
			want := make([]byte, 256*40+3)
			for i := range 256 * 40 {
				want[i] = byte(i % 256)
			}
			for _, workers := range []int{1, 4} {
				file, err := codec.Decode(context.Background(), v, workers)
				if err != nil {
					t.Fatal(err)
				}
				if file.Name != "payload.bin" || !bytes.Equal(file.Payload, want) {
					t.Fatalf("external recovery differs with %d workers", workers)
				}
			}
		})
	}
}
