package config

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/zeddD1abl0/netbox-trueup-ai/internal/glob"
)

// Problem is one thing wrong with the configuration. Its message never
// includes a secret's value.
type Problem struct {
	// Key is the setting, such as "netbox.url" or "sources[0].kind". It is
	// empty when the problem isn't with a single setting.
	Key string
	// Where says where the value came from, such as "env TRUEUP_NETBOX_URL"
	// or "file /etc/netbox-trueup-ai/config.yaml".
	Where string
	Msg   string
}

func (p Problem) String() string {
	prefix := p.Key
	switch {
	case p.Key != "" && p.Where != "":
		prefix += " (" + p.Where + ")"
	case p.Where != "":
		prefix = p.Where
	}
	if prefix == "" {
		return p.Msg
	}
	return prefix + ": " + p.Msg
}

// Error reports every problem Load found.
type Error struct {
	Problems []Problem
}

func (e *Error) Error() string {
	if len(e.Problems) == 1 {
		return "invalid configuration: " + e.Problems[0].String()
	}
	lines := make([]string, len(e.Problems))
	for i, p := range e.Problems {
		lines[i] = p.String()
	}
	return "invalid configuration:\n  " + strings.Join(lines, "\n  ")
}

func (ld *loader) logLevel(r resolved) slog.Level {
	switch strings.ToLower(r.str) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	ld.problem(r.key, r.where, fmt.Sprintf("unknown level %q: use debug, info, warn or error", r.str))
	return slog.LevelInfo
}

func (ld *loader) logFormat(r resolved) LogFormat {
	switch f := LogFormat(strings.ToLower(r.str)); f {
	case LogText, LogJSON:
		return f
	default:
		ld.problem(r.key, r.where, fmt.Sprintf("unknown format %q: use text or json", r.str))
		return LogText
	}
}

// databaseURL checks the database URL's scheme. The URL can hold a password,
// so messages never repeat it, nor url.Parse's errors, which quote it.
func (ld *loader) databaseURL(r resolved) Secret {
	if r.str == "" {
		return Secret{}
	}
	u, err := url.Parse(r.str)
	if err != nil {
		ld.problem(r.key, r.where, "is not a valid URL; percent-encode any special characters in the password")
		return Secret{}
	}
	switch u.Scheme {
	case "sqlite", "postgres", "postgresql":
		return NewSecret(r.str)
	default:
		ld.problem(r.key, r.where, "must be a sqlite, postgres or postgresql URL")
		return Secret{}
	}
}

// netboxURL checks the NetBox URL. Plain http is allowed, with a warning
// unless the host is this machine, because the token would cross the network
// unencrypted.
func (ld *loader) netboxURL(r resolved) string {
	if r.str == "" {
		return ""
	}
	if problem := NetBoxURLProblem(r.str); problem != "" {
		ld.problem(r.key, r.where, problem)
		return ""
	}
	u, _ := url.Parse(r.str) // NetBoxURLProblem parsed it
	if u.Scheme == "http" && !isLoopback(u.Hostname()) {
		ld.warn(r.key, fmt.Sprintf("netbox.url uses http, so the NetBox token crosses the network to %s unencrypted: use https", u.Host))
	}
	return r.str
}

// NetBoxURLProblem returns what is wrong with a NetBox URL, or "" if
// nothing is. The NetBox client checks its URL with it too, so that the two
// agree. The message never repeats the URL, which could hold a password.
func NetBoxURLProblem(raw string) string {
	u, err := url.Parse(raw)
	switch {
	case err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "":
		return "must be an https or http URL, such as https://netbox.example.com"
	case u.User != nil:
		return "must not contain a user name or password: set netbox.token instead"
	case u.RawQuery != "" || u.ForceQuery || u.Fragment != "":
		return "must not have a query or fragment"
	}
	return ""
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// netboxToken accepts only printable ASCII without spaces, which every NetBox
// token is made of. That catches what a token file or a paste can carry
// unseen, such as whitespace, a byte-order mark or a zero-width space, which
// NetBox would reject later with no pointer to the file.
//
// NetBox reads a token that starts with nbt_ as a v2 token, nbt_<key>.<token>,
// and refuses one without both parts (TokenAuthentication in
// netbox/netbox/api/authentication.py), so a token cut short on copying is
// caught here too.
func (ld *loader) netboxToken(r resolved) Secret {
	if problem := NetBoxTokenProblem(r.str); problem != "" {
		ld.problem(r.key, r.where, problem)
		return Secret{}
	}
	return NewSecret(r.str)
}

// NetBoxTokenProblem returns what is wrong with a NetBox token, or "" if
// nothing is. The NetBox client checks its token with it too, so that the
// two agree. The message never repeats the token.
func NetBoxTokenProblem(token string) string {
	if strings.ContainsFunc(token, func(c rune) bool { return c < 0x21 || c > 0x7e }) {
		return "contains characters a token can't, such as whitespace, control characters or a byte-order mark"
	}
	if rest, v2 := strings.CutPrefix(token, "nbt_"); v2 {
		if key, secret, ok := strings.Cut(rest, "."); !ok || key == "" || secret == "" {
			return "starts with nbt_, so it is a v2 token, but isn't in the form nbt_<key>.<token>: check that it was copied whole"
		}
	}
	return ""
}

// caBundle reads the certificates in a PEM file. Every block in it must be a
// certificate. A private key, say, is refused, since it doesn't belong there
// and its file was probably named by mistake; its content is never repeated.
// A block that can't be read, such as one cut short, is refused too, rather
// than quietly trusting only the others: pem.Decode skips such a block
// without a word, so the blocks it finds are counted against the lines that
// begin one.
func (ld *loader) caBundle(r resolved) []*x509.Certificate {
	if r.data == nil {
		return nil
	}
	var certs []*x509.Certificate
	rest := r.data
	for {
		var block *pem.Block
		if block, rest = pem.Decode(rest); block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			ld.problem(r.key, r.where, fmt.Sprintf("file %s holds a %q block: a CA bundle holds only certificates", r.str, block.Type))
			return nil
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			ld.problem(r.key, r.where, fmt.Sprintf("certificate %d in file %s can't be read: %v", len(certs)+1, r.str, err))
			return nil
		}
		certs = append(certs, cert)
	}
	switch {
	case pemBegins(r.data) != len(certs):
		ld.problem(r.key, r.where, fmt.Sprintf("file %s has a PEM block that can't be read, such as one cut short", r.str))
		return nil
	case len(certs) == 0:
		ld.problem(r.key, r.where, fmt.Sprintf("file %s holds no PEM certificates", r.str))
	}
	return certs
}

// pemBegins counts the lines in data that begin a PEM block.
func pemBegins(data []byte) int {
	n := 0
	for line := range bytes.Lines(data) {
		if bytes.HasPrefix(bytes.TrimLeft(line, " \t"), []byte("-----BEGIN ")) {
			n++
		}
	}
	return n
}

// duration reads a Go duration, such as 30s, between lo and hi. After a
// problem it returns def, so that loading can go on to find the others.
func (ld *loader) duration(r resolved, lo, hi, def time.Duration) time.Duration {
	d, err := time.ParseDuration(r.str)
	if err != nil {
		ld.problem(r.key, r.where, fmt.Sprintf("%q is not a duration: use a number and a unit, such as 30s or 2m", r.str))
		return def
	}
	if d < lo || d > hi {
		ld.problem(r.key, r.where, fmt.Sprintf("%s is out of range: use %s to %s", r.str, shortDuration(lo), shortDuration(hi)))
		return def
	}
	return d
}

// shortDuration writes 10m rather than time.Duration's 10m0s.
func shortDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}

// integer reads a whole number between lo and hi.
func (ld *loader) integer(r resolved, lo, hi, def int) int {
	n, err := strconv.Atoi(r.str)
	if errors.Is(err, strconv.ErrRange) {
		ld.problem(r.key, r.where, fmt.Sprintf("%s is out of range: use %d to %d", r.str, lo, hi))
		return def
	}
	if err != nil {
		ld.problem(r.key, r.where, fmt.Sprintf("%q is not a whole number", r.str))
		return def
	}
	if n < lo || n > hi {
		ld.problem(r.key, r.where, fmt.Sprintf("%d is out of range: use %d to %d", n, lo, hi))
		return def
	}
	return n
}

// sizeUnits are the units a size can be given in, in upper case. Only binary
// units are accepted, so that MB can't be read two ways.
var sizeUnits = map[string]int64{"": 1, "B": 1, "KIB": 1 << 10, "MIB": 1 << 20, "GIB": 1 << 30}

var sizePattern = regexp.MustCompile(`^([0-9]+) ?([A-Za-z]*)$`)

// size reads a number of bytes, such as 33554432 or 32MiB, between lo and hi.
func (ld *loader) size(r resolved, lo, hi, def int64) int64 {
	m := sizePattern.FindStringSubmatch(r.str)
	var unit int64
	if m != nil {
		unit = sizeUnits[strings.ToUpper(m[2])]
	}
	if unit == 0 {
		ld.problem(r.key, r.where, fmt.Sprintf("%q is not a size: use a number of bytes, or one with KiB, MiB or GiB, such as 32MiB", r.str))
		return def
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil || n > hi/unit || n*unit < lo {
		ld.problem(r.key, r.where, fmt.Sprintf("%s is out of range: use %s to %s", r.str, formatSize(lo), formatSize(hi)))
		return def
	}
	return n * unit
}

// formatSize writes a size in the largest binary unit that divides it.
func formatSize(n int64) string {
	for _, u := range []struct {
		name string
		size int64
	}{{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}} {
		if n >= u.size && n%u.size == 0 {
			return strconv.FormatInt(n/u.size, 10) + u.name
		}
	}
	return strconv.FormatInt(n, 10)
}

// sourceName keeps source names safe to use as directory names, in command
// lines and in changelog messages, and distinct on file systems that ignore
// case. A name can't end in a dot, which Windows strips from file names.
var sourceName = regexp.MustCompile(`^[a-z0-9]([a-z0-9._-]{0,61}[a-z0-9])?$`)

// windowsDevice reports whether Windows reserves name, with or without an
// extension, for a device, so that it can't be a directory's name.
func windowsDevice(name string) bool {
	base, _, _ := strings.Cut(name, ".")
	switch base {
	case "con", "prn", "aux", "nul":
		return true
	}
	return len(base) == 4 && (strings.HasPrefix(base, "com") || strings.HasPrefix(base, "lpt")) &&
		base[3] >= '0' && base[3] <= '9'
}

func (ld *loader) sources(r resolved) []Source {
	seen := map[string]bool{}
	for i, s := range r.sources {
		at := func(field string) string { return fmt.Sprintf("sources[%d].%s", i, field) }

		switch {
		case s.Name == "":
			ld.problem(at("name"), r.where, "is required")
		case !sourceName.MatchString(s.Name):
			ld.problem(at("name"), r.where, fmt.Sprintf("%q must be up to 63 lowercase letters, digits, dots, hyphens and underscores, starting and ending with a letter or digit", s.Name))
		case windowsDevice(s.Name):
			ld.problem(at("name"), r.where, fmt.Sprintf("%q is reserved for a device on Windows", s.Name))
		case seen[s.Name]:
			ld.problem(at("name"), r.where, fmt.Sprintf("%q is the name of another source", s.Name))
		}
		seen[s.Name] = true

		switch s.Kind {
		case "":
			ld.problem(at("kind"), r.where, "is required")
		case SourceKindGit:
		default:
			ld.problem(at("kind"), r.where, fmt.Sprintf("unknown kind %q: use git", s.Kind))
		}

		switch {
		case s.URL == "":
			ld.problem(at("url"), r.where, "is required")
		case s.Kind == SourceKindGit:
			if msg := GitURLProblem(s.URL); msg != "" {
				ld.problem(at("url"), r.where, msg)
			}
		}

		if s.Ref != "" && !validRef(s.Ref) {
			ld.problem(at("ref"), r.where, fmt.Sprintf("%q is not a valid Git branch or tag name", s.Ref))
		}

		switch s.Format {
		case "":
			ld.problem(at("format"), r.where, "is required")
		case FormatDeviceTypeLibrary:
		default:
			ld.problem(at("format"), r.where, fmt.Sprintf("unknown format %q: use devicetype-library", s.Format))
		}

		for _, list := range []struct {
			field    string
			patterns []string
		}{{"include", s.Include}, {"exclude", s.Exclude}} {
			for j, p := range list.patterns {
				if msg := glob.Problem(p); msg != "" {
					ld.problem(fmt.Sprintf("%s[%d]", at(list.field), j), r.where, msg)
				}
			}
		}

		limit := func(field, value string) resolved {
			return resolved{key: at(field), where: r.where, str: value}
		}
		r.sources[i].Limits = SourceLimits{
			MaxFileSize: ld.size(limit("max_file_size", s.MaxFileSize),
				minSourceMaxFileSize, maxSourceMaxFileSize, defaultSourceMaxFileSize),
			MaxFiles: ld.integer(limit("max_files", s.MaxFiles),
				1, maxSourceMaxFiles, defaultSourceMaxFiles),
			FetchTimeout: ld.duration(limit("fetch_timeout", s.FetchTimeout),
				minSourceFetchTimeout, maxSourceFetchTimeout, defaultSourceFetchTimeout),
			MaxFetchSize: ld.size(limit("max_fetch_size", s.MaxFetchSize),
				minSourceMaxFetchSize, maxSourceMaxFetchSize, defaultSourceMaxFetchSize),
		}
	}
	return r.sources
}

// scpURL matches Git's scp-like syntax, such as git@github.com:org/repo.git.
// Neither the user nor the host may start with a hyphen.
var scpURL = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*@[A-Za-z0-9][A-Za-z0-9.-]*:\S+$`)

// GitURLProblem returns what is wrong with a Git source URL, or "" if nothing
// is. Only https and ssh are allowed (B1 in docs/architecture.md), and
// credentials don't belong in the URL: an https URL can't have a user name,
// which GitHub and GitLab accept as a token, and an ssh URL can name a user
// but not a password. A host or user starting with a hyphen is refused,
// because git and ssh would read it as an option. The Git source kind checks
// its URL with it too, so that the two agree. Messages never repeat the URL,
// which could hold a secret.
func GitURLProblem(raw string) string {
	if !strings.Contains(raw, "://") {
		if scpURL.MatchString(raw) {
			return ""
		}
		return "must be an https or ssh URL, such as https://github.com/netbox-community/devicetype-library.git or git@github.com:netbox-community/devicetype-library.git"
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "is not a valid URL"
	}
	switch u.Scheme {
	case "https":
		if u.User != nil {
			return "must not contain a user name or password"
		}
	case "ssh":
		if _, hasPassword := u.User.Password(); hasPassword {
			return "must not contain a password"
		}
	default:
		return "must use https or ssh"
	}
	if u.Hostname() == "" {
		return "must name a host"
	}
	if strings.HasPrefix(u.Hostname(), "-") || strings.HasPrefix(u.User.Username(), "-") {
		return "must not have a host or user name starting with -"
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "must not have a query or fragment, which can carry a token"
	}
	return ""
}

// validRef applies the rules of git check-ref-format that a branch or tag
// name can break, and rejects a leading hyphen, which a command line would
// read as an option.
func validRef(ref string) bool {
	if ref == "@" ||
		strings.HasPrefix(ref, "-") || strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, ".") ||
		strings.HasSuffix(ref, "/") || strings.HasSuffix(ref, ".") || strings.HasSuffix(ref, ".lock") ||
		strings.Contains(ref, "..") || strings.Contains(ref, "//") || strings.Contains(ref, "/.") ||
		strings.Contains(ref, "@{") {
		return false
	}
	return !strings.ContainsFunc(ref, func(c rune) bool {
		return unicode.IsSpace(c) || unicode.IsControl(c) || strings.ContainsRune(`~^:?*[\`, c)
	})
}
