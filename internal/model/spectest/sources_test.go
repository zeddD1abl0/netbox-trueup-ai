package spectest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/model"
)

// source is a Python file shaped like NetBox's, with the things that can
// mislead a search for definitions.
const source = `from django.db import models

CONSTANT = 1
LIMIT: int = 2

class Thing(models.Model):
    """
A docstring with a line that isn't indented.
    """
    name = models.CharField(
        verbose_name='name',
    )
# a comment at the start of a line, inside the class
    def clean(self):
        if self.name == 'x':
            pass

class Other(models.Model):
    def save(self):
        pass

class ThingChoices(ChoiceSet):
    key = 'Thing.kind'
    CHOICES = [('a', 'A')]

class PlainChoices(ChoiceSet):
    CHOICES = [('a', 'A')]
`

func TestSources(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "test"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "test", "models.py"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "test", "empty.py"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	choice := func(symbol, key string) model.Field {
		return model.Field{
			Name: "kind", Kind: model.KindChoice,
			Choices: &model.Choices{Values: []string{"a"}, Configurable: key},
			Source:  model.Source{File: "test/models.py", Symbol: symbol},
		}
	}
	tests := []struct {
		name   string
		symbol string
		field  model.Field
		want   string // part of the problem, or empty for none
	}{
		{name: "a class", symbol: "Thing"},
		{name: "a method after a docstring and a comment", symbol: "Thing.clean"},
		{name: "a constant", symbol: "CONSTANT"},
		{name: "an annotated constant", symbol: "LIMIT"},
		{name: "a method of another class", symbol: "Other.clean", want: "doesn't define clean"},
		{name: "a keyword argument", symbol: "Thing.verbose_name", want: "doesn't define verbose_name"},
		{name: "a keyword argument at the top", symbol: "verbose_name", want: "doesn't define verbose_name"},
		{name: "a comparison", symbol: "Thing.clean.self", want: "doesn't define self"},
		{name: "a missing class", symbol: "Missing", want: "doesn't define Missing"},
		{name: "a configurable choice set", symbol: "Thing", field: choice("ThingChoices", "test.Thing.kind")},
		{name: "a choice set that can't change", symbol: "Thing", field: choice("PlainChoices", "")},
		{name: "a key it hasn't got", symbol: "Thing", field: choice("PlainChoices", "test.Plain.kind"), want: `its FIELD_CHOICES key is "test.Plain.kind", but PlainChoices's is ""`},
		{name: "another key", symbol: "Thing", field: choice("ThingChoices", "test.Thing.type"), want: `but ThingChoices's is "test.Thing.kind"`},
		{name: "a class that isn't a choice set", symbol: "Thing", field: choice("Other", ""), want: "it cites Other, which isn't a choice set"},
	}
	for _, tt := range tests {
		s := &model.Spec{Type: "test.thing", Sources: []model.Source{{File: "test/models.py", Symbol: tt.symbol}}}
		if tt.field.Name != "" {
			s.Fields = []model.Field{tt.field}
		}
		err := Sources(root, []*model.Spec{s})
		switch {
		case tt.want == "" && err != nil:
			t.Errorf("%s: Sources = %v", tt.name, err)
		case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
			t.Errorf("%s: Sources = %v, want a problem with %q", tt.name, err, tt.want)
		}
	}
	// An empty file defines nothing.
	err := Sources(root, []*model.Spec{{Type: "test.thing", Sources: []model.Source{{File: "test/empty.py", Symbol: "Thing"}}}})
	if err == nil || !strings.Contains(err.Error(), "doesn't define Thing") {
		t.Errorf("Sources of a symbol in an empty file = %v", err)
	}
}
