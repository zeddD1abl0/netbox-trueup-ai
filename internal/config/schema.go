package config

import (
	"fmt"
	"strconv"
	"strings"
	"time"

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
	kindFile         // a path, like kindPath, to a file that is read when settings load
	kindScalar       // a string, or in the config file a number, which is read as its decimal text
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

// NetBox client defaults and limits. The client's defaults for options left
// at zero match these, apart from retries, which a test in internal/netbox
// checks.
const (
	defaultNetBoxTimeout     = 30 * time.Second
	minNetBoxTimeout         = time.Second
	maxNetBoxTimeout         = 10 * time.Minute
	defaultNetBoxMaxRequests = 4
	maxNetBoxMaxRequests     = 32
	defaultNetBoxRetries     = 3
	maxNetBoxRetries         = 10
	defaultNetBoxMaxResponse = 32 << 20 // 32 MiB
	minNetBoxMaxResponse     = 1 << 20  // 1 MiB
	maxNetBoxMaxResponse     = 1 << 30  // 1 GiB
)

// Defaults and bounds of each source's limits (docs/configuration.md).
// devicetype-library's largest YAML file is 19 KB, and the library has
// about 17,600 files.
const (
	defaultSourceMaxFileSize  = 1 << 20 // 1 MiB
	minSourceMaxFileSize      = 1 << 10 // 1 KiB
	maxSourceMaxFileSize      = 64 << 20
	defaultSourceMaxFiles     = 50000
	maxSourceMaxFiles         = 1000000
	defaultSourceFetchTimeout = 15 * time.Minute
	minSourceFetchTimeout     = 10 * time.Second
	maxSourceFetchTimeout     = 2 * time.Hour
	defaultSourceMaxFetchSize = 2 << 30 // 2 GiB
	minSourceMaxFetchSize     = 16 << 20
	maxSourceMaxFetchSize     = 64 << 30
)

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
		key: "netbox.ca_bundle", kind: kindFile, flag: true,
		usage: "PEM file of CA certificates to trust for NetBox, as well as the system's",
		apply: func(ld *loader, st *Settings, r resolved) {
			st.NetBox.CABundle, st.NetBox.CACerts = r.str, ld.caBundle(r)
		},
	},
	{
		key: "netbox.timeout", kind: kindScalar, flag: true, def: defaultNetBoxTimeout.String(),
		usage: "limit on each NetBox request, reading the answer included",
		apply: func(ld *loader, st *Settings, r resolved) {
			st.NetBox.Timeout = ld.duration(r, minNetBoxTimeout, maxNetBoxTimeout, defaultNetBoxTimeout)
		},
	},
	{
		key: "netbox.max_requests", kind: kindScalar, flag: true, def: strconv.Itoa(defaultNetBoxMaxRequests),
		usage: "how many NetBox requests may be in flight at once",
		apply: func(ld *loader, st *Settings, r resolved) {
			st.NetBox.MaxRequests = ld.integer(r, 1, maxNetBoxMaxRequests, defaultNetBoxMaxRequests)
		},
	},
	{
		key: "netbox.retries", kind: kindScalar, flag: true, def: strconv.Itoa(defaultNetBoxRetries),
		usage: "how many more times a failed NetBox read is tried",
		apply: func(ld *loader, st *Settings, r resolved) {
			st.NetBox.Retries = ld.integer(r, 0, maxNetBoxRetries, defaultNetBoxRetries)
		},
	},
	{
		key: "netbox.max_response_size", kind: kindScalar, flag: true, def: "32MiB",
		usage: "the most TrueUp reads of one NetBox answer, such as 32MiB",
		apply: func(ld *loader, st *Settings, r resolved) {
			st.NetBox.MaxResponseSize = ld.size(r, minNetBoxMaxResponse, maxNetBoxMaxResponse, defaultNetBoxMaxResponse)
		},
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
	switch {
	case r.file:
		return "a string"
	case r.setting.kind == kindSources:
		return "a list of sources"
	case r.setting.kind == kindScalar:
		return "a string or a number"
	default:
		return "a string"
	}
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
