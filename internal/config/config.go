// Package config loads TrueUp's declared settings: command-line flags, then
// TRUEUP_ environment variables, then a YAML config file, then defaults
// (ADR 0012). It records which layer each setting came from, so that
// `config show` and, later, the web UI can say so.
package config

import (
	"log/slog"
)

// Settings are the effective, validated settings.
type Settings struct {
	Log      Log
	DataDir  string // absolute path
	Database Database
	NetBox   NetBox
	Sources  []Source
}

// Log configures logging.
type Log struct {
	Level  slog.Level
	Format LogFormat
}

// LogFormat is how log records are written.
type LogFormat string

// Log formats.
const (
	LogText LogFormat = "text" // for people at a terminal
	LogJSON LogFormat = "json" // for log pipelines
)

// Database configures TrueUp's own database.
type Database struct {
	// URL is empty for a SQLite database in the data directory.
	URL Secret
}

// NetBox configures the connection to NetBox.
type NetBox struct {
	URL   string
	Token Secret
}

// Source is where definitions come from.
type Source struct {
	Name    string   `json:"name"`
	Kind    string   `json:"kind"`
	URL     string   `json:"url"`
	Ref     string   `json:"ref"` // empty for the repository's default branch
	Format  string   `json:"format"`
	Include []string `json:"include"`
	Exclude []string `json:"exclude"`
}

// Source kinds and formats that milestone 1 supports.
const (
	SourceKindGit           = "git"
	FormatDeviceTypeLibrary = "devicetype-library"
)

// Origin is the layer a setting's value came from.
type Origin int

// Origins, lowest precedence first. Runtime settings stored in the database
// will slot in between OriginDefault and OriginFile (ADR 0012).
const (
	OriginDefault Origin = iota
	OriginFile
	OriginEnv
	OriginFlag
)

func (o Origin) String() string {
	switch o {
	case OriginDefault:
		return "default"
	case OriginFile:
		return "file"
	case OriginEnv:
		return "env"
	case OriginFlag:
		return "flag"
	default:
		return "unknown"
	}
}

// Declared reports whether the value was declared in a flag, the environment
// or the config file. A declared runtime setting is read-only in the UI.
func (o Origin) Declared() bool {
	return o >= OriginFile
}

// Config is the result of Load.
type Config struct {
	Settings Settings
	// File is the config file that was read.
	File File
	// Warnings are problems that don't stop TrueUp from running. Log them
	// once logging is set up.
	Warnings []Warning

	entries []Entry
}

// File says which config file was read, and why.
type File struct {
	Path   string // empty if no file was read
	Origin Origin // OriginFlag for --config, OriginEnv for TRUEUP_CONFIG, otherwise OriginDefault
}

// Warning is a problem with the configuration that doesn't stop TrueUp.
type Warning struct {
	Key     string
	Message string
}

// Entry is one setting as `config show` displays it.
type Entry struct {
	Key string
	// Value is a string, or []Source for sources. A secret's value is
	// "[redacted]" when it is set, and empty when it isn't.
	Value  any
	Secret bool
	Origin Origin
	// Name is how the winning layer named the setting: a flag such as
	// "--log-level", an environment variable such as "TRUEUP_LOG_LEVEL", or
	// the key in the config file. It is empty for defaults.
	Name string
	// File is the path a secret was read from, when it was given by its
	// _file companion.
	File string
}

// Entries returns every setting in a fixed order, with secrets redacted.
// Changing what it returns changes nothing else.
func (c *Config) Entries() []Entry {
	out := make([]Entry, len(c.entries))
	for i, e := range c.entries {
		if sources, ok := e.Value.([]Source); ok {
			e.Value = cloneSources(sources)
		}
		out[i] = e
	}
	return out
}
