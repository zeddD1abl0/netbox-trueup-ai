package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"

	koanfyaml "github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/providers/posflag"
	"github.com/knadh/koanf/v2"
	"github.com/spf13/pflag"
	"go.yaml.in/yaml/v3"
)

// Size limits for files Load reads.
const (
	maxConfigFileSize  = 1 << 20  // 1 MiB
	maxSecretFileSize  = 64 << 10 // 64 KiB
	maxSettingFileSize = 1 << 20  // 1 MiB, for files such as a CA bundle
)

// Input says where Load finds declared settings.
type Input struct {
	// Flags is the parsed command line, with flags defined by RegisterFlags.
	// Only flags the user set are read. It may be nil.
	Flags *pflag.FlagSet
	// Environ is the environment, as os.Environ returns it.
	Environ []string
	// DefaultConfigFile is read, if it exists, when neither --config nor
	// TRUEUP_CONFIG names a file. Normally DefaultConfigFile().
	DefaultConfigFile string
}

// Load reads every layer of declared settings, resolves each setting to the
// highest layer that sets it, and validates the result. All the problems it
// finds are returned together as an *Error. If ctx ends first, Load returns
// its error instead.
func Load(ctx context.Context, in Input) (*Config, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ld := &loader{tainted: map[string]bool{}}
	file := ld.configFile(ctx, in)
	// Lowest precedence first. Runtime settings stored in the database will
	// be read as a layer between the defaults and the file (ADR 0012). The
	// database is found through database.url and data_dir, so those will be
	// resolved from the declared layers first.
	layers := []*layer{defaultsLayer()}
	if file.Path != "" {
		layers = append(layers, ld.fileLayer(ctx, file.Path))
	}
	layers = append(layers, ld.envLayer(in.Environ), ld.flagLayer(in.Flags))
	cfg := ld.build(ctx, layers)

	if err := ctx.Err(); err != nil {
		return nil, err // interrupted, which isn't a problem with the configuration
	}
	if len(ld.problems) > 0 {
		return nil, &Error{Problems: ld.problems}
	}
	cfg.File = file
	cfg.Warnings = ld.warnings
	return cfg, nil
}

// loader collects problems and warnings while loading.
type loader struct {
	problems []Problem
	warnings []Warning
	// tainted holds the settings that already have a problem in some layer.
	// They aren't validated, so that one mistake isn't reported twice.
	tainted map[string]bool
}

func (ld *loader) problem(key, where, msg string) {
	ld.problems = append(ld.problems, Problem{Key: key, Where: where, Msg: msg})
}

// reject records a problem with a setting's value in one layer.
func (ld *loader) reject(setting, key, where, msg string) {
	ld.tainted[setting] = true
	ld.problem(key, where, msg)
}

func (ld *loader) warn(key, msg string) {
	ld.warnings = append(ld.warnings, Warning{Key: key, Message: msg})
}

// layer holds the settings that one layer sets.
type layer struct {
	origin Origin
	file   string           // for OriginFile, the file's path
	values map[string]value // by setting key; a secret's _file companion is stored under the secret's key
	// holdsSecret is set when the layer gives a secret's value directly,
	// rather than through its _file companion.
	holdsSecret bool
}

// value is a setting as one layer sets it.
type value struct {
	v    any    // a string, or []Source for sources
	name string // how the layer named it: "--log-level", "TRUEUP_LOG_LEVEL" or "log.level"
	file bool   // set through a secret's _file companion: v is the path to read the secret from
}

func newLayer(origin Origin) *layer {
	return &layer{origin: origin, values: map[string]value{}}
}

// where says where a value named name in this layer came from, for messages.
func (l *layer) where(name string) string {
	switch l.origin {
	case OriginFlag:
		return "flag " + name
	case OriginEnv:
		return "env " + name
	case OriginFile:
		return "file " + l.file
	case OriginDefault:
		return "default"
	}
	return l.origin.String()
}

// describe names the layer as a whole, for messages.
func (l *layer) describe() string {
	switch l.origin {
	case OriginFlag:
		return "flags"
	case OriginEnv:
		return "environment"
	case OriginFile:
		return "file " + l.file
	case OriginDefault:
		return "defaults"
	}
	return l.origin.String()
}

// configFile decides which config file to read: the one --config names,
// else the one TRUEUP_CONFIG names, else the default file if it exists.
// The working directory is never searched.
func (ld *loader) configFile(ctx context.Context, in Input) File {
	if ctx.Err() != nil {
		return File{}
	}
	if in.Flags != nil {
		if f := in.Flags.Lookup(FlagConfig); f != nil && f.Changed && f.Value.String() != "" {
			return ld.namedFile(f.Value.String(), OriginFlag)
		}
	}
	if p := lookupEnv(in.Environ, EnvConfigFile); p != "" {
		return ld.namedFile(p, OriginEnv)
	}
	if in.DefaultConfigFile == "" {
		return File{}
	}
	// Only a file that doesn't exist is skipped. Anything else, such as a
	// symbolic link to nowhere or permission denied, is reported when the
	// file is read, so that TrueUp never quietly runs without its file.
	if _, err := os.Lstat(in.DefaultConfigFile); errors.Is(err, fs.ErrNotExist) {
		return File{}
	}
	return File{Path: in.DefaultConfigFile, Origin: OriginDefault}
}

func (ld *loader) namedFile(path string, origin Origin) File {
	abs, err := filepath.Abs(path)
	if err != nil {
		ld.problem("", "", fmt.Sprintf("config file %s: %v", path, err))
		return File{}
	}
	return File{Path: abs, Origin: origin}
}

// lookupEnv returns the value of the last entry for name in environ.
func lookupEnv(environ []string, name string) string {
	var v string
	for _, kv := range environ {
		if k, val, ok := strings.Cut(kv, "="); ok && canonicalEnv(k) == name {
			v = val
		}
	}
	return v
}

// canonicalEnv returns the name to look an environment variable up by.
// Windows ignores the case of variable names, but keeps whatever case they
// were set in, so there names are compared in upper case.
func canonicalEnv(name string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(name)
	}
	return name
}

func defaultsLayer() *layer {
	l := newLayer(OriginDefault)
	for _, s := range settings {
		if s.def != "" {
			l.values[s.key] = value{v: s.def}
		}
	}
	return l
}

// bytesProvider hands koanf a file that Load has already read, with limits.
type bytesProvider []byte

func (b bytesProvider) ReadBytes() ([]byte, error) { return b, nil }

func (b bytesProvider) Read() (map[string]any, error) {
	return nil, errors.New("bytesProvider needs a parser")
}

func (ld *loader) fileLayer(ctx context.Context, path string) *layer {
	l := newLayer(OriginFile)
	l.file = path
	data, worldReadable, err := readFile(ctx, path, maxConfigFileSize)
	if err != nil {
		ld.problem("", "", "read config file: "+err.Error())
		return l
	}
	if !ld.checkYAML(data, l.describe()) {
		return l
	}
	k := koanf.New(".")
	if err := k.Load(bytesProvider(data), koanfyaml.Parser()); err != nil {
		// checkYAML has ruled out what makes decoding fail, so this
		// shouldn't happen. The error could quote a value, so it isn't
		// repeated.
		ld.problem("", l.describe(), "is not valid YAML")
		return l
	}

	base := filepath.Dir(path)
	flat := k.All()
	mappings := map[string]bool{} // settings given a mapping, already reported
	for _, key := range k.Keys() {
		raw := flat[key]
		if sections[key] {
			// "netbox:" with nothing under it is fine; "netbox: x" isn't.
			if m, isMap := raw.(map[string]any); raw != nil && (!isMap || len(m) > 0) {
				ld.problem(key, l.where(key), "must be a mapping of settings")
			}
			continue
		}
		// koanf flattens a mapping given where a value belongs, such as
		// sources' fields without the list, into keys below the setting.
		// Report the setting once, rather than each key as unknown.
		if above, ref, ok := settingAbove(key); ok {
			if !mappings[above] {
				mappings[above] = true
				ld.reject(ref.setting.key, above, l.where(above), "must be "+ref.want()+", not a mapping")
			}
			continue
		}
		ld.set(l, key, raw, key, base)
	}
	if l.holdsSecret && worldReadable {
		ld.warn("", fmt.Sprintf("config file %s holds a secret and every user on this host can read it: remove read access for others (chmod o-r), or give the secret through its _file setting", path))
	}
	return l
}

// checkYAML checks the config file before koanf reads it, so that no YAML
// error can repeat a secret. The YAML library quotes values in some of its
// errors: decoding a document that isn't a mapping, a value whose explicit
// tag doesn't fit it, an alias to an unknown anchor. Any of those could be a
// secret, such as a token in the file, or a secret file that --config names
// by mistake. So the file must be:
//
//   - one document, since anything after a second "---" would be ignored;
//   - a mapping, or empty;
//   - free of tags, anchors and aliases, which settings have no need for;
//   - keyed by plain strings, each key once per mapping, and none with a
//     dot. koanf would merge "netbox.url" with a url nested under netbox
//     without a word, and one value would silently win.
//
// Messages give lines and keys, never values.
func (ld *loader) checkYAML(data []byte, where string) bool {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	var doc yaml.Node
	switch err := dec.Decode(&doc); {
	case errors.Is(err, io.EOF):
		return true // an empty file
	case err != nil:
		ld.problem("", where, yamlSyntaxError(err))
		return false
	}
	var next yaml.Node
	switch err := dec.Decode(&next); {
	case err == nil:
		ld.problem("", where, "must hold a single YAML document")
		return false
	case !errors.Is(err, io.EOF):
		ld.problem("", where, yamlSyntaxError(err))
		return false
	}

	if len(doc.Content) == 0 {
		return true // comments only
	}
	root := doc.Content[0]
	if root.Kind == yaml.ScalarNode && root.Tag == "!!null" && root.Style&yaml.TaggedStyle == 0 && root.Anchor == "" {
		return true // "---" and comments only
	}
	if root.Kind != yaml.MappingNode {
		ld.problem("", where, "must be a YAML mapping of settings")
		return false
	}
	return ld.checkNode(root, where)
}

// yamlSyntaxError returns the message for an error from parsing YAML into
// nodes. Those are syntax errors with fixed text, apart from an alias to an
// unknown anchor, which quotes the anchor's name: that could be the rest of
// a secret starting with "*".
func yamlSyntaxError(err error) string {
	if strings.Contains(err.Error(), "unknown anchor") {
		return "uses a YAML alias, which settings don't support"
	}
	return err.Error()
}

func (ld *loader) checkNode(n *yaml.Node, where string) bool {
	ok := true
	fail := func(line int, msg string) {
		ld.problem("", where, fmt.Sprintf("line %d: %s", line, msg))
		ok = false
	}
	if n.Style&yaml.TaggedStyle != 0 {
		fail(n.Line, "YAML tags, such as !!str, aren't supported")
	}
	if n.Anchor != "" || n.Kind == yaml.AliasNode {
		fail(n.Line, "YAML anchors and aliases aren't supported")
	}
	switch n.Kind {
	case yaml.MappingNode:
		seen := map[string]int{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i]
			switch {
			case key.Kind != yaml.ScalarNode || key.Tag != "!!str":
				fail(key.Line, "keys must be plain strings")
			case strings.Contains(key.Value, "."):
				fail(key.Line, fmt.Sprintf("key %q contains a dot: nest it under its section instead", key.Value))
			case seen[key.Value] != 0:
				fail(key.Line, fmt.Sprintf("key %q is already set at line %d", key.Value, seen[key.Value]))
			default:
				seen[key.Value] = key.Line
			}
			ok = ld.checkNode(key, where) && ok
			ok = ld.checkNode(n.Content[i+1], where) && ok
		}
	case yaml.SequenceNode:
		for _, item := range n.Content {
			ok = ld.checkNode(item, where) && ok
		}
	default:
	}
	return ok
}

func (ld *loader) envLayer(environ []string) *layer {
	l := newLayer(OriginEnv)
	var unknown []string
	k := koanf.New(".")
	err := k.Load(env.Provider(".", env.Opt{
		// No Prefix: it would compare case-sensitively, and a name like
		// trueup_log_level should be reported, not ignored.
		EnvironFunc: func() []string { return environ },
		TransformFunc: func(name, v string) (string, any) {
			if len(name) < len(EnvPrefix) || !strings.EqualFold(name[:len(EnvPrefix)], EnvPrefix) {
				return "", nil
			}
			name = canonicalEnv(name)
			if name == EnvConfigFile {
				return "", nil // read by configFile
			}
			key, ok := byEnv[name]
			if !ok {
				unknown = append(unknown, name)
				return "", nil
			}
			if v == "" {
				return "", nil // an empty variable counts as unset
			}
			if byKey[key].setting.kind == kindSources {
				var list any
				if err := json.Unmarshal([]byte(v), &list); err != nil {
					ld.reject(key, key, l.where(name), "must be a JSON list of sources: "+err.Error())
					return "", nil
				}
				if _, isList := list.([]any); list != nil && !isList {
					ld.reject(key, key, l.where(name), "must be a JSON list of sources")
					return "", nil
				}
				return key, list
			}
			return key, v
		},
	}), nil)
	if err != nil {
		ld.problem("", l.describe(), err.Error())
		return l
	}
	slices.Sort(unknown)
	for _, name := range unknown {
		msg := "unknown setting"
		if upper := strings.ToUpper(name); upper != name && (byEnv[upper] != "" || upper == EnvConfigFile) {
			msg += fmt.Sprintf("; names are upper case: %s", upper)
		}
		ld.problem("", l.where(name), msg)
	}
	flat := k.All()
	for _, key := range k.Keys() {
		ld.set(l, key, flat[key], envName(key), "")
	}
	return l
}

func (ld *loader) flagLayer(flags *pflag.FlagSet) *layer {
	l := newLayer(OriginFlag)
	if flags == nil {
		return l
	}
	k := koanf.New(".")
	// With a nil koanf instance, posflag skips flags the user didn't set.
	err := k.Load(posflag.ProviderWithFlag(flags, ".", nil, func(f *pflag.Flag) (string, any) {
		key, ok := byFlag[f.Name]
		if !ok || f.Value.String() == "" {
			return "", nil
		}
		return key, f.Value.String()
	}), nil)
	if err != nil {
		ld.problem("", l.describe(), err.Error())
		return l
	}
	flat := k.All()
	for _, key := range k.Keys() {
		ld.set(l, key, flat[key], "--"+flagName(key), "")
	}
	return l
}

// set records one value from a layer, after checking its key and type.
// Relative paths are resolved against base, or the working directory if base
// is empty.
func (ld *loader) set(l *layer, key string, raw any, name, base string) {
	ref, ok := byKey[key]
	if !ok {
		ld.problem(key, l.where(name), "unknown setting")
		return
	}
	if raw == nil {
		return // YAML null counts as unset
	}
	s := ref.setting
	k := s.kind
	if ref.file {
		k = kindPath
	}

	var v any
	switch k {
	case kindString, kindPath, kindFile, kindScalar:
		str, isString := raw.(string)
		if !isString && k == kindScalar {
			str, isString = numberText(raw)
		}
		if !isString {
			ld.reject(s.key, key, l.where(name), "must be "+ref.want())
			return
		}
		if str == "" {
			return // an empty string counts as unset
		}
		if k == kindPath || k == kindFile {
			p, err := absPath(str, base)
			if err != nil {
				ld.reject(s.key, key, l.where(name), err.Error())
				return
			}
			str = p
		}
		v = str
	case kindSources:
		list, ok := ld.decodeSources(raw, l.where(name))
		if !ok {
			ld.tainted[s.key] = true
			return
		}
		v = list
	}

	if prev, dup := l.values[s.key]; dup {
		ld.reject(s.key, "", l.describe(), fmt.Sprintf("%s and %s are both set; set only one", prev.name, name))
		return
	}
	l.values[s.key] = value{v: v, name: name, file: ref.file}
	if s.secret && !ref.file {
		l.holdsSecret = true
	}
}

// numberText returns a number from the config file as decimal text, which is
// how the environment and flags give it.
func numberText(raw any) (string, bool) {
	switch n := raw.(type) {
	case int:
		return strconv.Itoa(n), true
	case int64:
		return strconv.FormatInt(n, 10), true
	case uint64:
		return strconv.FormatUint(n, 10), true
	case float64:
		return strconv.FormatFloat(n, 'f', -1, 64), true
	default:
		return "", false
	}
}

func absPath(p, base string) (string, error) {
	if filepath.IsAbs(p) {
		return filepath.Clean(p), nil
	}
	if base != "" {
		return filepath.Join(base, p), nil
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("resolve relative path %s: %w", p, err)
	}
	return abs, nil
}

// decodeSources decodes the sources list from a file or the environment.
func (ld *loader) decodeSources(raw any, where string) ([]Source, bool) {
	items, isList := raw.([]any)
	if !isList {
		ld.problem("sources", where, "must be a list of sources")
		return nil, false
	}
	ok := true
	out := make([]Source, 0, len(items))
	for i, item := range items {
		prefix := fmt.Sprintf("sources[%d]", i)
		fields, isMap := item.(map[string]any)
		if !isMap {
			ld.problem(prefix, where, "must be a mapping of source fields")
			ok = false
			continue
		}
		s := Source{Include: []string{}, Exclude: []string{}}
		for _, name := range slices.Sorted(maps.Keys(fields)) {
			key, v := prefix+"."+name, fields[name]
			var good bool
			switch name {
			case "name":
				s.Name, good = ld.str(key, where, v)
			case "kind":
				s.Kind, good = ld.str(key, where, v)
			case "url":
				s.URL, good = ld.str(key, where, v)
			case "ref":
				s.Ref, good = ld.str(key, where, v)
			case "format":
				s.Format, good = ld.str(key, where, v)
			case "include":
				s.Include, good = ld.strs(key, where, v)
			case "exclude":
				s.Exclude, good = ld.strs(key, where, v)
			default:
				ld.problem(key, where, "unknown field")
			}
			ok = ok && good
		}
		out = append(out, s)
	}
	return out, ok
}

func (ld *loader) str(key, where string, v any) (string, bool) {
	if v == nil {
		return "", true
	}
	s, ok := v.(string)
	if !ok {
		ld.problem(key, where, "must be a string")
	}
	return s, ok
}

func (ld *loader) strs(key, where string, v any) ([]string, bool) {
	out := []string{}
	if v == nil {
		return out, true
	}
	items, ok := v.([]any)
	if !ok {
		ld.problem(key, where, "must be a list of strings")
		return out, false
	}
	for i, item := range items {
		s, ok := item.(string)
		if !ok {
			ld.problem(fmt.Sprintf("%s[%d]", key, i), where, "must be a string")
			return out, false
		}
		out = append(out, s)
	}
	return out, true
}

// resolved is a setting's winning value, ready for its apply function.
type resolved struct {
	key   string
	where string // where the value came from, for messages
	str   string // for every kind but sources
	// data is the content of a kindFile setting's file. It is nil if the
	// setting isn't set, or if the file couldn't be read, which has been
	// reported.
	data    []byte
	sources []Source // for sources
}

// build resolves every setting to the highest layer that sets it, reads
// secrets from their files, and validates the results. layers[0] must be
// the defaults.
func (ld *loader) build(ctx context.Context, layers []*layer) *Config {
	cfg := &Config{}
	for _, s := range settings {
		if ld.tainted[s.key] {
			continue
		}
		top, v := winner(layers, s.key)
		e := Entry{Key: s.key, Secret: s.secret, Origin: top.origin, Name: v.name}
		r := resolved{key: s.key, where: top.where(v.name)}
		switch s.kind {
		case kindString, kindPath, kindFile, kindScalar:
			r.str, _ = v.v.(string)
			e.Value = r.str
			if s.secret {
				if v.file {
					e.File = r.str
					r.str = ld.readSecret(ctx, s.key, r.where, r.str)
				}
				e.Value = NewSecret(r.str).String()
			}
			if s.kind == kindFile && r.str != "" {
				r.data = ld.readSettingFile(ctx, s.key, r.where, r.str)
			}
		case kindSources:
			r.sources, _ = v.v.([]Source)
			if r.sources == nil {
				r.sources = []Source{}
			}
			e.Value = cloneSources(r.sources)
		}
		s.apply(ld, &cfg.Settings, r)
		cfg.entries = append(cfg.entries, e)
	}
	return cfg
}

// winner returns the highest layer that sets key, and its value. A setting
// that no layer sets comes from the defaults, with no value.
func winner(layers []*layer, key string) (*layer, value) {
	for _, l := range slices.Backward(layers) {
		if v, ok := l.values[key]; ok {
			return l, v
		}
	}
	return layers[0], value{}
}

// readSecret reads a secret from the file at path. A trailing line break is
// dropped, since most ways of writing a secret to a file add one.
func (ld *loader) readSecret(ctx context.Context, key, where, path string) string {
	data, worldReadable, err := readFile(ctx, path, maxSecretFileSize)
	if err != nil {
		ld.problem(key, where, "read secret file: "+err.Error())
		return ""
	}
	secret := strings.TrimRight(string(data), "\r\n")
	if secret == "" {
		ld.problem(key, where, fmt.Sprintf("secret file %s is empty", path))
		return ""
	}
	if worldReadable {
		ld.warn(key, fmt.Sprintf("secret file %s can be read by every user on this host: remove read access for others, with chmod o-r or the mode of a Kubernetes or Docker secret mount", path))
	}
	return secret
}

// readSettingFile reads the file a kindFile setting names, or returns nil if
// it can't, having reported why.
func (ld *loader) readSettingFile(ctx context.Context, key, where, path string) []byte {
	data, _, err := readFile(ctx, path, maxSettingFileSize)
	if err != nil {
		ld.problem(key, where, "read file: "+err.Error())
		return nil
	}
	return data
}

// readFile reads at most limit bytes from the file at path, and reports
// whether every user on the host can read it. Mode bits don't describe
// Windows ACLs, so there it never reports the file as readable by all.
func readFile(ctx context.Context, path string, limit int64) (data []byte, worldReadable bool, err error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	// security: path comes from the operator's own flags, environment or
	// config file, inside the deployment's trust boundary (B5 in
	// docs/architecture.md), so it isn't confined to a directory.
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	if info.IsDir() {
		return nil, false, fmt.Errorf("%s is a directory", path)
	}
	data, err = io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, false, fmt.Errorf("read %s: %w", path, err)
	}
	if int64(len(data)) > limit {
		return nil, false, fmt.Errorf("%s is larger than %d bytes", path, limit)
	}
	worldReadable = runtime.GOOS != "windows" && info.Mode().Perm()&0o004 != 0
	return data, worldReadable, nil
}

func cloneSources(in []Source) []Source {
	out := make([]Source, len(in))
	for i, s := range in {
		s.Include = slices.Clone(s.Include)
		s.Exclude = slices.Clone(s.Exclude)
		out[i] = s
	}
	return out
}
