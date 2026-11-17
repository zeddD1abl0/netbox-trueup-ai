package format

import (
	"errors"
	"strconv"
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
		// NetBox's import reads a float as Django's IntegerField does:
		// int(str(value)) with a trailing .0 stripped.
		{model.KindInteger, "1.0", model.Integer(1), ""},
		{model.KindInteger, "2.", model.Integer(2), ""},
		{model.KindInteger, "-0.0", model.Integer(0), ""},
		{model.KindInteger, "1_000.0", model.Integer(1000), ""},
		{model.KindInteger, "1:30.0", model.Integer(90), ""},
		{model.KindInteger, "1.0e+3", model.Integer(1000), ""},
		{model.KindInteger, "2.0000000000000001", model.Integer(2), ""}, // the float is 2.0
		{model.KindInteger, "9999999999999998.0", model.Integer(9999999999999998), ""},
		{model.KindInteger, "2.5", nil, "is 2.5, but NetBox's f is a whole number"},
		// str() gives 1e+16, which the message doesn't show, as the file
		// doesn't give it.
		{model.KindInteger, "1.0e+16", nil, "is a whole number written with a decimal point, which NetBox's import refuses once it is 1e16 or more, or -1e16 or less: write it without the decimal point"},
		{model.KindInteger, "-1.0e+16", nil, "NetBox's import refuses"},
		{model.KindInteger, "'2'", nil, "is text, but NetBox's f is a whole number"},
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

// TestWholeFloatKeepsWhyItCantRead checks that a number strconv can't read,
// which resolvePlain doesn't give, keeps strconv's reason.
func TestWholeFloatKeepsWhyItCantRead(t *testing.T) {
	t.Parallel()
	_, err := wholeFloat(&model.Field{Name: "f", Kind: model.KindInteger}, "1.0x")
	if !errors.Is(err, strconv.ErrSyntax) {
		t.Errorf("wholeFloat(1.0x) = %v, want strconv.ErrSyntax", err)
	}
}
