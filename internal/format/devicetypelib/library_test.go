package devicetypelib

import (
	"flag"
	"os"
	"testing"
	"time"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/format"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/source/sourcetest"
)

var library = flag.String("library", "", "a checkout of devicetype-library to read whole, such as .cache/devicetype-library")

// TestWholeLibrary reads a whole checkout of devicetype-library, when
// -library names one, and fails on any file it finds invalid, since the
// library's own tests pass them all. Warnings are logged. Run it after a
// change to the adapter, or to see what a new release of the library brings:
//
//	git clone --depth 1 --filter=blob:none --sparse \
//	    https://github.com/netbox-community/devicetype-library.git .cache/devicetype-library
//	git -C .cache/devicetype-library sparse-checkout set device-types schema
//	go test ./internal/format/devicetypelib -run WholeLibrary -v -library "$PWD/.cache/devicetype-library"
func TestWholeLibrary(t *testing.T) {
	if *library == "" {
		t.Skip("-library names no checkout of devicetype-library")
	}
	limits := sourcetest.Limits
	limits.MaxFiles = 1000000
	snap := sourcetest.SnapshotOf(t, sourcetest.Kind{FS: os.DirFS(*library), Version: "checkout"},
		config.Source{Name: "devicetype-library", Kind: "test", Include: []string{"device-types", "schema"}, Limits: limits})
	start := time.Now()
	res, err := Format{}.Read(t.Context(), snap)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("read %d files in %s: %d definitions, %d invalid, %d problems", res.Files, time.Since(start).Round(time.Millisecond), len(res.Definitions), res.Invalid, len(res.Problems))
	for _, p := range res.Problems {
		if p.Severity == format.Error {
			t.Errorf("%s", p)
		} else {
			t.Logf("warning: %s", p)
		}
	}
}
