package config

import (
	"fmt"
	"io"
	"log/slog"
)

const redacted = "[redacted]"

// Secret holds a sensitive setting, such as the NetBox token. It prints, logs
// and marshals as "[redacted]", so that printing or logging Settings can't
// leak it. Call Reveal for the value itself.
type Secret struct {
	// value is behind a pointer because fmt prints a struct's unexported
	// fields by reflection, without calling Format. A struct that holds a
	// Secret in an unexported field then prints an address, not the value.
	value *string
}

func newSecret(v string) Secret {
	if v == "" {
		return Secret{}
	}
	return Secret{value: &v}
}

// Reveal returns the secret's value. Pass it only to what needs it, such as
// an Authorization header, and never to a log or an error.
func (s Secret) Reveal() string {
	if s.value == nil {
		return ""
	}
	return *s.value
}

// IsSet reports whether the secret has a value.
func (s Secret) IsSet() bool {
	return s.value != nil
}

// String returns "[redacted]" for a secret that is set, and "" otherwise.
func (s Secret) String() string {
	if !s.IsSet() {
		return ""
	}
	return redacted
}

// Format makes every fmt verb, %#v included, print the redacted form.
func (s Secret) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, s.String())
}

// LogValue makes slog log the redacted form.
func (s Secret) LogValue() slog.Value {
	return slog.StringValue(s.String())
}

// MarshalText makes encoding/json and other encoders write the redacted form.
func (s Secret) MarshalText() ([]byte, error) {
	return []byte(s.String()), nil
}
