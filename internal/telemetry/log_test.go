package telemetry

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
)

func TestNewLogger(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  config.Log
		want []string // lines logged, in order
	}{
		{
			name: "text at info",
			cfg:  config.Log{Level: slog.LevelInfo, Format: config.LogText},
			want: []string{`level=INFO msg=hello n=1`, `level=WARN msg=careful`},
		},
		{
			name: "text at warn drops info",
			cfg:  config.Log{Level: slog.LevelWarn, Format: config.LogText},
			want: []string{`level=WARN msg=careful`},
		},
		{
			name: "json at debug",
			cfg:  config.Log{Level: slog.LevelDebug, Format: config.LogJSON},
			want: []string{`"level":"DEBUG","msg":"detail"`, `"level":"INFO","msg":"hello","n":1`, `"level":"WARN","msg":"careful"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			logger, err := NewLogger(&buf, tt.cfg)
			if err != nil {
				t.Fatalf("NewLogger: %v", err)
			}
			logger.Debug("detail")
			logger.Info("hello", "n", 1)
			logger.Warn("careful")

			lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
			if len(lines) != len(tt.want) {
				t.Fatalf("logged %d lines, want %d:\n%s", len(lines), len(tt.want), buf.String())
			}
			for i, want := range tt.want {
				if !strings.Contains(lines[i], want) {
					t.Errorf("line %d = %s, want it to contain %s", i, lines[i], want)
				}
				if tt.cfg.Format == config.LogJSON && !json.Valid([]byte(lines[i])) {
					t.Errorf("line %d isn't JSON: %s", i, lines[i])
				}
			}
		})
	}
}

func TestNewLoggerRejectsUnknownFormat(t *testing.T) {
	t.Parallel()

	if _, err := NewLogger(&bytes.Buffer{}, config.Log{Format: "xml"}); err == nil {
		t.Error("NewLogger accepted format xml")
	}
}
