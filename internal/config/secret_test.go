package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestSecretNeverPrints(t *testing.T) {
	t.Parallel()

	const value = "nbt_example.s3cret"
	settings := Settings{
		NetBox:   NetBox{URL: "https://netbox.example.com", Token: newSecret(value)},
		Database: Database{URL: newSecret("postgres://trueup:" + value + "@db.example.com/trueup")},
	}

	var text, js bytes.Buffer
	slog.New(slog.NewTextHandler(&text, nil)).Info("settings", "settings", settings, "token", settings.NetBox.Token)
	slog.New(slog.NewJSONHandler(&js, nil)).Info("settings", "settings", settings, "token", settings.NetBox.Token)
	marshalled, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	outputs := map[string]string{
		"%v":           fmt.Sprintf("%v", settings),
		"%+v":          fmt.Sprintf("%+v", settings),
		"%#v":          fmt.Sprintf("%#v", settings),
		"%s":           fmt.Sprintf("%s", settings.NetBox.Token),
		"%q":           fmt.Sprintf("%q", settings.NetBox.Token),
		"%x":           fmt.Sprintf("%x", settings.NetBox.Token),
		"String":       settings.NetBox.Token.String(),
		"slog text":    text.String(),
		"slog JSON":    js.String(),
		"json.Marshal": string(marshalled),
	}
	for name, out := range outputs {
		if strings.Contains(out, "s3cret") {
			t.Errorf("%s leaks the secret: %s", name, out)
		}
		if !strings.Contains(out, redacted) {
			t.Errorf("%s doesn't show %s: %s", name, redacted, out)
		}
	}

	if got := settings.NetBox.Token.Reveal(); got != value {
		t.Errorf("Reveal() = %q, want %q", got, value)
	}
}

// fmt prints unexported fields by reflection, without calling Format, as a
// client holding the token in a private field would.
func TestSecretInAnUnexportedField(t *testing.T) {
	t.Parallel()

	type client struct {
		token    Secret
		settings Settings
	}
	c := client{
		token:    newSecret("nbt_example.s3cret"),
		settings: Settings{NetBox: NetBox{Token: newSecret("nbt_example.s3cret")}},
	}
	var text bytes.Buffer
	slog.New(slog.NewTextHandler(&text, nil)).Info("client", "client", c)

	for name, out := range map[string]string{
		"%v":        fmt.Sprintf("%v", c),
		"%+v":       fmt.Sprintf("%+v", c),
		"%#v":       fmt.Sprintf("%#v", c),
		"slog text": text.String(),
	} {
		if strings.Contains(out, "s3cret") {
			t.Errorf("%s leaks the secret: %s", name, out)
		}
	}
}

func TestUnsetSecret(t *testing.T) {
	t.Parallel()

	var s Secret
	if s.IsSet() {
		t.Error("zero Secret is set")
	}
	if got := s.String(); got != "" {
		t.Errorf("zero Secret prints %q, want %q", got, "")
	}
	if !newSecret("x").IsSet() {
		t.Error("Secret with a value isn't set")
	}
}
