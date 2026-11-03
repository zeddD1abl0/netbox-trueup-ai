package format

import (
	"strings"
	"testing"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
)

func TestValue(t *testing.T) {
	t.Parallel()
	date, err := model.ParseDate("2027-03-31")
	if err != nil {
		t.Fatal(err)
	}
	field := func(kind model.Kind) *model.Field { return &model.Field{Name: "f", Kind: kind} }
	tests := []struct {
		kind    model.Kind
		yaml    string
		want    model.Value
		wantErr string
	}{
		{model.KindString, "text", model.String("text"), ""},
		{model.KindString, "'010'", model.String("010"), ""},
		{model.KindString, "010", nil, "is a whole number, but NetBox's f is text"},
		{model.KindChoice, "front-to-rear", model.Choice("front-to-rear"), ""},
		{model.KindChoice, "yes", nil, "is true or false, but NetBox's f is a choice"},
		{model.KindDate, "'2027-03-31'", date, ""},
		{model.KindDate, "'31/03/2027'", nil, "isn't a date"},
		{model.KindBoolean, "on", model.Boolean(true), ""},
		{model.KindBoolean, "'true'", nil, "is text, but NetBox's f is true or false"},
		{model.KindDecimal, "1.50", model.NewDecimal(15, 1), ""},
		{model.KindDecimal, "16", model.NewDecimal(16, 0), ""},
		{model.KindDecimal, "1:30", model.NewDecimal(90, 0), ""},
		{model.KindDecimal, "1.0e+20", nil, "more than 18 significant digits"},
		{model.KindDecimal, "'1.5'", nil, "is text, but NetBox's f is a number"},
		{model.KindInteger, "0x1F", model.Integer(31), ""},
		{model.KindInteger, "1.0", nil, "is a number, but NetBox's f is a whole number"},
		{model.KindString, "~", model.Null{}, ""},
		{model.KindString, "[a]", nil, "is a list, but NetBox's f is text"},
		{model.KindReference, "Dell", nil, "TrueUp doesn't read NetBox's f"},
	}
	for _, tt := range tests {
		root, err := ParseYAML([]byte("v: "+tt.yaml+"\n"), DefinitionLimits)
		if err != nil {
			t.Fatalf("ParseYAML(%q): %v", tt.yaml, err)
		}
		got, err := Value(field(tt.kind), root.Get("v"))
		switch {
		case tt.wantErr != "":
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Value(%s, %s) = %v, %v; want an error holding %q", tt.kind, tt.yaml, got, err, tt.wantErr)
			}
		case err != nil || !model.Equal(got, tt.want):
			t.Errorf("Value(%s, %s) = %v, %v; want %v", tt.kind, tt.yaml, got, err, tt.want)
		}
	}
}
