package devicetypelib

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/format"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
	"github.com/zeddD1abl0/netbox-trueup-ai/internal/source/sourcetest"
)

// FuzzReadDeviceType checks that no file makes the adapter panic, or give a
// definition that NetBox's rules would refuse, and that every problem says
// where it is and stays short.
func FuzzReadDeviceType(f *testing.F) {
	seeds, err := filepath.Glob("testdata/library/device-types/*/*")
	if err != nil {
		f.Fatal(err)
	}
	for _, p := range seeds {
		data, err := os.ReadFile(p) //nolint:gosec // G304: the package's own testdata.
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	f.Add([]byte(valid + "airflow: front-to-rear\nweight: 1.5\nweight_unit: kg\ncomments: \"\\u202e\"\n"))
	f.Add([]byte("manufacturer: \" Acme\"\nmodel: 010\nslug: yes\nu_height: 1:30\nis_full_depth: on\n"))
	snap := sourcetest.Snapshot(f, "lib", librarySchemas(f))
	schema, err := compileSchema(f.Context(), snap)
	if err != nil {
		f.Fatal(err)
	}
	r := newReader(schema)
	f.Fuzz(func(t *testing.T, data []byte) {
		const p = "device-types/Acme/box.yaml"
		def, problems := r.read(p, data)
		valid := true
		for _, pr := range problems {
			if pr.Path != p || pr.Reason == "" || len(pr.String()) > 1000 || pr.Severity != format.Error && pr.Severity != format.Warning {
				t.Fatalf("problem %+v", pr)
			}
			valid = valid && pr.Severity != format.Error
		}
		if !valid {
			return
		}
		for name, v := range def.Object.Fields {
			field := r.deviceType.Field(name)
			if field == nil {
				t.Fatalf("a valid file gives %s, which isn't a field", name)
			}
			// An airflow NetBox doesn't ship with is a warning.
			if err := field.Validate(v); err != nil && name != "airflow" {
				t.Fatalf("a valid file gives %s, which NetBox refuses: %v", v, err)
			}
			if !model.Equal(field.Normalize(v), v) {
				t.Fatalf("a valid file gives %s for %s, not normalized", v, name)
			}
		}
		for _, name := range r.deviceType.Identity {
			if def.Identity[name] == nil {
				t.Fatalf("a valid file has no %s", name)
			}
		}
	})
}
