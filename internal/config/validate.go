package config

import (
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"
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
	u, err := url.Parse(r.str)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		ld.problem(r.key, r.where, "must be an https or http URL, such as https://netbox.example.com")
		return ""
	}
	if u.User != nil {
		ld.problem(r.key, r.where, "must not contain a user name or password: set netbox.token instead")
		return ""
	}
	if u.RawQuery != "" || u.Fragment != "" {
		ld.problem(r.key, r.where, "must not have a query or fragment")
		return ""
	}
	if u.Scheme == "http" && !isLoopback(u.Hostname()) {
		ld.warn(r.key, fmt.Sprintf("netbox.url uses http, so the NetBox token crosses the network to %s unencrypted: use https", u.Host))
	}
	return r.str
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
func (ld *loader) netboxToken(r resolved) Secret {
	if strings.ContainsFunc(r.str, func(c rune) bool { return c < 0x21 || c > 0x7e }) {
		ld.problem(r.key, r.where, "contains characters a token can't, such as whitespace, control characters or a byte-order mark")
		return Secret{}
	}
	return NewSecret(r.str)
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
			if msg := checkGitURL(s.URL); msg != "" {
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
				if msg := checkPattern(p); msg != "" {
					ld.problem(fmt.Sprintf("%s[%d]", at(list.field), j), r.where, msg)
				}
			}
		}
	}
	return r.sources
}

// scpURL matches Git's scp-like syntax, such as git@github.com:org/repo.git.
// Neither the user nor the host may start with a hyphen.
var scpURL = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*@[A-Za-z0-9][A-Za-z0-9.-]*:\S+$`)

// checkGitURL returns what is wrong with a Git source URL, or "". Only https
// and ssh are allowed (B1 in docs/architecture.md), and credentials don't
// belong in the URL: an https URL can't have a user name, which GitHub and
// GitLab accept as a token, and an ssh URL can name a user but not a
// password. A host or user starting with a hyphen is refused, because git
// and ssh would read it as an option. Messages never repeat the URL, which
// could hold a secret.
func checkGitURL(raw string) string {
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

// checkPattern returns what is wrong with an include or exclude pattern, or
// "". Patterns are matched against paths inside the source.
func checkPattern(p string) string {
	switch {
	case p == "":
		return "must not be empty"
	case strings.HasPrefix(p, "/"):
		return "must be relative to the top of the source, with no leading /"
	case slices.Contains(strings.Split(p, "/"), ".."):
		return "must not contain .."
	}
	return ""
}
