package config

import (
	"fmt"
	"strings"

	"github.com/spf13/pflag"
)

// EnvPrefix starts the name of every environment variable TrueUp reads.
const EnvPrefix = "TRUEUP_"

// EnvConfigFile and FlagConfig name the config file to read. They aren't
// settings themselves: they say where settings come from.
const (
	EnvConfigFile = EnvPrefix + "CONFIG"
	FlagConfig    = "config"
)

// fileSuffix ends the key of a secret's companion setting, which names a
// file to read the secret from.
const fileSuffix = "_file"

// kind is how a setting's value is written.
type kind int

const (
	kindString  kind = iota
	kindPath         // a file system path, made absolute when it is loaded
	kindSources      // the list of sources
)

// setting describes one declared setting.
type setting struct {
	key    string // snake_case key path, such as "netbox.url"
	kind   kind
	secret bool   // never displayed, and has a key+"_file" companion
	flag   bool   // can be set on the command line; for a secret, only through its _file companion
	def    string // default value
	usage  string // help text for the flag
	// apply validates the resolved value and stores it in Settings.
	apply func(ld *loader, st *Settings, r resolved)
}

// settings lists every setting, in the order `config show` prints them.
// Environment variable and flag names are derived from the keys, and a test
// checks that no two settings share one.
var settings = []setting{
	{
		key: "log.level", kind: kindString, flag: true, def: "info",
		usage: "lowest level to log: debug, info, warn or error",
		apply: func(ld *loader, st *Settings, r resolved) { st.Log.Level = ld.logLevel(r) },
	},
	{
		key: "log.format", kind: kindString, flag: true, def: string(LogText),
		usage: "log format: text or json",
		apply: func(ld *loader, st *Settings, r resolved) { st.Log.Format = ld.logFormat(r) },
	},
	{
		key: "data_dir", kind: kindPath, flag: true, def: defaultDataDir,
		usage: "directory for the database and source caches",
		apply: func(_ *loader, st *Settings, r resolved) { st.DataDir = r.str },
	},
	{
		key: "database.url", kind: kindString, secret: true, flag: true,
		usage: "file to read the database URL from (default: SQLite in the data directory)",
		apply: func(ld *loader, st *Settings, r resolved) { st.Database.URL = ld.databaseURL(r) },
	},
	{
		key: "netbox.url", kind: kindString, flag: true,
		usage: "NetBox URL, such as https://netbox.example.com",
		apply: func(ld *loader, st *Settings, r resolved) { st.NetBox.URL = ld.netboxURL(r) },
	},
	{
		key: "netbox.token", kind: kindString, secret: true, flag: true,
		usage: "file to read the NetBox API token from",
		apply: func(ld *loader, st *Settings, r resolved) { st.NetBox.Token = ld.netboxToken(r) },
	},
	{
		key: "sources", kind: kindSources,
		apply: func(ld *loader, st *Settings, r resolved) { st.Sources = ld.sources(r) },
	},
}

// keyRef is a key that a layer may set: a setting, or a secret's _file
// companion.
type keyRef struct {
	setting setting
	file    bool
}

var (
	byKey    = map[string]keyRef{} // every key a layer may set
	byEnv    = map[string]string{} // environment variable name to key
	byFlag   = map[string]string{} // flag name to key
	sections = map[string]bool{}   // keys that only group other keys, such as "netbox"
)

func init() {
	for _, s := range settings {
		keys := []string{s.key}
		if s.secret {
			keys = append(keys, s.fileKey())
		}
		for _, k := range keys {
			byKey[k] = keyRef{setting: s, file: k != s.key}
			byEnv[envName(k)] = k
		}
		if s.flag {
			byFlag[flagName(s.flagKey())] = s.flagKey()
		}
		parts := strings.Split(s.key, ".")
		for i := 1; i < len(parts); i++ {
			sections[strings.Join(parts[:i], ".")] = true
		}
	}
}

// want describes the value the key takes, for messages.
func (r keyRef) want() string {
	if r.setting.kind == kindSources && !r.file {
		return "a list of sources"
	}
	return "a string"
}

// settingAbove returns the key of the setting that key lies below, and what
// it refers to: "sources.name" lies below sources.
func settingAbove(key string) (string, keyRef, bool) {
	for i := strings.LastIndex(key, "."); i > 0; i = strings.LastIndex(key[:i], ".") {
		if ref, ok := byKey[key[:i]]; ok {
			return key[:i], ref, true
		}
	}
	return "", keyRef{}, false
}

// fileKey is the key of a secret's _file companion.
func (s setting) fileKey() string {
	return s.key + fileSuffix
}

// flagKey is the key that the setting's flag sets. A secret's flag sets its
// _file companion, so that secrets never appear on a command line, where
// other users of the host can see them.
func (s setting) flagKey() string {
	if s.secret {
		return s.fileKey()
	}
	return s.key
}

// envName is the environment variable for a key: the prefix, then the key in
// upper case with dots as underscores. "database.url" is TRUEUP_DATABASE_URL.
func envName(key string) string {
	return EnvPrefix + strings.ToUpper(strings.ReplaceAll(key, ".", "_"))
}

// flagName is the flag for a key, with dots and underscores as hyphens.
// "data_dir" is --data-dir.
func flagName(key string) string {
	return strings.NewReplacer(".", "-", "_", "-").Replace(key)
}

// RegisterFlags defines --config and a flag for each setting that has one.
// Load reads only the flags the user set.
func RegisterFlags(fs *pflag.FlagSet) {
	fs.String(FlagConfig, "", fmt.Sprintf("config file to read (default %s, if it exists)", defaultConfigFile))
	for _, s := range settings {
		if s.flag {
			fs.String(flagName(s.flagKey()), s.def, s.usage)
		}
	}
}

// DefaultConfigFile is the config file read when neither --config nor
// TRUEUP_CONFIG names one, if it exists.
func DefaultConfigFile() string {
	return defaultConfigFile
}

// DefaultDataDir is the default for data_dir.
func DefaultDataDir() string {
	return defaultDataDir
}
