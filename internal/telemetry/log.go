// Package telemetry sets up logging. Metrics and traces join it in
// milestone 6.
package telemetry

import (
	"fmt"
	"io"
	"log/slog"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/config"
)

// NewLogger returns a logger that writes records at the configured level and
// above to w, as text for terminals or as JSON for log pipelines.
//
// Secrets are kept out of logs by their type: config.Secret logs as
// "[redacted]".
func NewLogger(w io.Writer, cfg config.Log) (*slog.Logger, error) {
	opts := &slog.HandlerOptions{Level: cfg.Level}
	switch cfg.Format {
	case config.LogText:
		return slog.New(slog.NewTextHandler(w, opts)), nil
	case config.LogJSON:
		return slog.New(slog.NewJSONHandler(w, opts)), nil
	default:
		return nil, fmt.Errorf("unknown log format %q", cfg.Format)
	}
}
