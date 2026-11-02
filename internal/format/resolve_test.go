package format

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"
)

// TestResolveAsPyYAML checks resolvePlain against what PyYAML's SafeLoader
// makes of each plain scalar in testdata/pyyaml.json, which
// testdata/pyyaml.py wrote by running it: the same type and value, or an
// error where PyYAML gives what NetBox can't hold, or fails.
func TestResolveAsPyYAML(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("testdata/pyyaml.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Scalar string `json:"scalar"`
		Tag    string `json:"tag"`
		Value  string `json:"value"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 1000 {
		t.Fatalf("testdata/pyyaml.json holds %d cases, too few to trust", len(cases))
	}
	tags := map[string]Tag{"str": TagStr, "int": TagInt, "float": TagFloat, "bool": TagBool, "null": TagNull}
	for _, c := range cases {
		tag, value, err := resolvePlain(c.Scalar)
		if c.Error != "" {
			if err == nil {
				t.Errorf("resolvePlain(%q) = %s %q, but PyYAML gives %s", c.Scalar, tag, value, c.Error)
			}
			continue
		}
		if err != nil {
			t.Errorf("resolvePlain(%q): %v, but PyYAML gives %s %q", c.Scalar, err, c.Tag, c.Value)
			continue
		}
		if tag != tags[c.Tag] {
			t.Errorf("resolvePlain(%q) is %s, but PyYAML makes it %s %q", c.Scalar, tag, c.Tag, c.Value)
			continue
		}
		if tag == TagFloat {
			// Go and Python write the shortest decimal alike, but switch
			// to an exponent at different sizes: compare the numbers.
			got, err1 := strconv.ParseFloat(value, 64)
			want, err2 := strconv.ParseFloat(c.Value, 64)
			if err1 != nil || err2 != nil || got != want {
				t.Errorf("resolvePlain(%q) = %s, but PyYAML makes it %s", c.Scalar, value, c.Value)
			}
			continue
		}
		if value != c.Value {
			t.Errorf("resolvePlain(%q) = %q, but PyYAML makes it %q", c.Scalar, value, c.Value)
		}
	}
}

// TestResolveNumbersShortest checks that a number is written in its
// shortest decimal form, which is what model.ParseDecimal reads and what
// NetBox's import makes of PyYAML's float.
func TestResolveNumbersShortest(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"12.21":               "12.21",
		"1.10":                "1.1",
		"1.00000000000000001": "1",
		"0.30000000000000004": "0.30000000000000004",
		"1.0e+3":              "1000",
		"-0.5":                "-0.5",
		".5":                  "0.5",
		"1:30.5":              "90.5",
		"1_000.25":            "1000.25",
	} {
		tag, got, err := resolvePlain(in)
		if err != nil || tag != TagFloat || got != want {
			t.Errorf("resolvePlain(%q) = %s %q, %v; want a number, %q", in, tag, got, err, want)
		}
	}
}
