package netboxtest

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

//go:embed compose.yaml
var composeFile []byte

// buildContexts holds the directories compose.yaml's services build from,
// every file in them, as Compose sends them to Docker. TestBuildContexts
// checks that they match the directories on disk.
//
//go:embed all:branching
var buildContexts embed.FS

// Profile names a variant of the stack: which NetBox runs on the shared
// PostgreSQL and Valkey. Start runs the one NETBOXTEST_PROFILE names.
type Profile string

const (
	// Stock runs netbox-docker's image as it is.
	Stock Profile = "stock"
	// Branching runs that image with the netbox-branching plugin added, and
	// the background worker that provisions branches.
	Branching Profile = "branching"
)

// profiles lists every profile, each of which must run a NetBox.
var profiles = []Profile{Stock, Branching}

// workerService is the service that runs NetBox's background worker, in the
// profiles that have one.
const workerService = "netbox-worker"

// stack is the part of the Compose file format that netboxtest can run the
// same way Docker Compose does. Decoding rejects every other key, so
// compose.yaml can't use a feature that the tests would silently ignore.
type stack struct {
	Name     string             `yaml:"name"`
	Services map[string]service `yaml:"services"`
}

// plan is what runs for one profile: the services in start order, every
// service after the services it depends on, and the one that runs NetBox's
// web server.
type plan struct {
	order []string
	web   string
}

type service struct {
	// Profiles is empty for a service every profile runs, such as the
	// database, or names the one profile that runs it.
	Profiles    []string              `yaml:"profiles"`
	Image       string                `yaml:"image"`
	Build       *build                `yaml:"build"`
	User        string                `yaml:"user"`
	Command     []string              `yaml:"command"`
	Environment map[string]string     `yaml:"environment"`
	DependsOn   map[string]dependency `yaml:"depends_on"`
	Expose      []string              `yaml:"expose"`
	Ports       []string              `yaml:"ports"`
	Healthcheck *healthcheck          `yaml:"healthcheck"`
}

type dependency struct {
	Condition string `yaml:"condition"`
}

// build builds a service's image from a directory beside compose.yaml, with
// the Dockerfile in it.
type build struct {
	Context string `yaml:"context"`
}

type healthcheck struct {
	Test          []string `yaml:"test"`
	StartPeriod   duration `yaml:"start_period"`
	StartInterval duration `yaml:"start_interval"`
	Interval      duration `yaml:"interval"`
	Timeout       duration `yaml:"timeout"`
	Retries       int      `yaml:"retries"`
}

// duration is a Compose duration, such as "1m30s". Compose writes durations
// the way Go does.
type duration time.Duration

func (d *duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	if v <= 0 {
		return fmt.Errorf("line %d: duration %q must be positive", n.Line, s)
	}
	*d = duration(v)
	return nil
}

// readiness is how long Docker takes to mark a service unhealthy that never
// passes its health check. Waiting that long matches `docker compose up --wait`.
func (h *healthcheck) readiness() time.Duration {
	return time.Duration(h.StartPeriod) + time.Duration(h.Interval+h.Timeout)*time.Duration(h.Retries)
}

// parseStack decodes and checks a Compose file, whose services build from
// directories in contexts. It returns the stack and what runs for each profile.
func parseStack(data []byte, contexts fs.FS) (*stack, map[Profile]plan, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, nil, err
	}
	if errs := checkStrings(&doc, ""); len(errs) > 0 {
		return nil, nil, errors.Join(errs...)
	}

	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var st stack
	if err := dec.Decode(&st); err != nil {
		return nil, nil, fmt.Errorf("%w (netboxtest reads only the Compose keys it can run the same way; see its README)", err)
	}
	var extra yaml.Node
	switch err := dec.Decode(&extra); {
	case err == nil:
		return nil, nil, errors.New("the file must hold exactly one YAML document")
	case !errors.Is(err, io.EOF):
		return nil, nil, fmt.Errorf("after the first YAML document: %w", err)
	}

	if len(st.Services) == 0 {
		return nil, nil, errors.New("no services")
	}
	var problems []error
	for _, name := range slices.Sorted(maps.Keys(st.Services)) {
		for _, err := range st.Services[name].check(st.Services, contexts) {
			problems = append(problems, fmt.Errorf("service %s: %w", name, err))
		}
	}
	if len(problems) > 0 {
		return nil, nil, errors.Join(problems...)
	}

	plans := map[Profile]plan{}
	for _, p := range profiles {
		pl, err := st.plan(p)
		if err != nil {
			problems = append(problems, fmt.Errorf("profile %s: %w", p, err))
			continue
		}
		plans[p] = pl
	}
	if len(problems) > 0 {
		return nil, nil, errors.Join(problems...)
	}
	return &st, plans, nil
}

// inProfile reports whether profile p runs the service.
func (s service) inProfile(p Profile) bool {
	return len(s.Profiles) == 0 || slices.Contains(s.Profiles, string(p))
}

// plan works out what runs for profile p: the services in start order, and
// the one that runs NetBox's web server, which is the one service that
// publishes a port.
func (st *stack) plan(p Profile) (plan, error) {
	services := map[string]service{}
	var web []string
	for name, s := range st.Services {
		if !s.inProfile(p) {
			continue
		}
		services[name] = s
		if len(s.Ports) > 0 {
			web = append(web, name)
		}
	}
	slices.Sort(web)
	if len(web) != 1 {
		return plan{}, fmt.Errorf("needs exactly one service that publishes ports, for NetBox's web server, not %d (%s)", len(web), strings.Join(web, ", "))
	}
	nb := services[web[0]]
	var problems []error
	if len(nb.Ports) != 1 {
		problems = append(problems, fmt.Errorf("service %s: needs exactly one entry in ports, for NetBox's web server", web[0]))
	}
	for _, key := range []string{"SUPERUSER_NAME", "SUPERUSER_PASSWORD"} {
		if nb.Environment[key] == "" {
			problems = append(problems, fmt.Errorf("service %s: environment needs %s, to provision tokens with", web[0], key))
		}
	}
	if len(problems) > 0 {
		return plan{}, errors.Join(problems...)
	}
	order, err := startOrder(services)
	if err != nil {
		return plan{}, err
	}
	return plan{order: order, web: web[0]}, nil
}

// checkStrings reports values that YAML reads as something other than a
// string. Compose turns a number or boolean back into text its own way (01
// becomes "1", True becomes "true") while netboxtest keeps the text as
// written, so such values must be quoted. retries, a count, is the exception.
func checkStrings(n *yaml.Node, key string) []error {
	var errs []error
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, c := range n.Content {
			errs = append(errs, checkStrings(c, key)...)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			errs = append(errs, checkStrings(n.Content[i+1], n.Content[i].Value)...)
		}
	case yaml.ScalarNode:
		want := "!!str"
		if key == "retries" {
			want = "!!int"
		}
		// An empty value is null; parseStack reports it with more context.
		if tag := n.ShortTag(); tag != want && tag != "!!null" {
			errs = append(errs, fmt.Errorf("line %d: %s %q is a YAML %s, not a %s; quote it, since Compose and netboxtest read other types differently",
				n.Line, key, n.Value, strings.TrimPrefix(tag, "!!"), strings.TrimPrefix(want, "!!")))
		}
	case yaml.AliasNode:
		errs = append(errs, fmt.Errorf("line %d: aliases aren't supported", n.Line))
	}
	return errs
}

func (s service) check(all map[string]service, contexts fs.FS) []error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	switch {
	case len(s.Profiles) > 1:
		add("is in %d profiles; a service is in one profile, or in none to run in every profile", len(s.Profiles))
	case len(s.Profiles) == 1 && !slices.Contains(profiles, Profile(s.Profiles[0])):
		add("profile %q isn't one netboxtest runs (%s)", s.Profiles[0], profileNames())
	}

	switch {
	case s.Build != nil && s.Image != "":
		add("has both image and build; give one, as Compose would otherwise tag what it builds with the image's name")
	case s.Build != nil:
		if _, err := dockerfileBase(contexts, s.Build.Context); err != nil {
			add("build: %w", err)
		}
	case !isPinned(s.Image):
		// Dependabot needs the tag to propose updates, and the digest makes the
		// pull reproducible.
		add("image %q must name a tag and pin a digest, as name:tag@sha256:<digest>", s.Image)
	}

	// Compose substitutes ${VAR} and $VAR from the shell running it, which the
	// tests wouldn't. Only a published host port may use it, because tests
	// choose their own.
	for _, v := range slices.Concat([]string{s.Image, s.User}, s.Command, s.Expose) {
		if strings.Contains(v, "$") {
			add("%q uses $, which Compose would substitute from the shell", v)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(s.Environment)) {
		switch v := s.Environment[k]; {
		case v == "":
			add("environment %s is empty; Compose would take it from the shell", k)
		case strings.Contains(v, "$"):
			add("environment %s uses $, which Compose would substitute from the shell", k)
		}
	}

	for _, dep := range slices.Sorted(maps.Keys(s.DependsOn)) {
		target, ok := all[dep]
		switch {
		case !ok:
			add("depends on %s, which isn't a service", dep)
		case s.DependsOn[dep].Condition != "service_healthy":
			add("depends_on %s needs condition: service_healthy", dep)
		case target.Healthcheck == nil:
			add("depends on %s, which has no healthcheck", dep)
		// A service can rely only on services that run whenever it does.
		case len(target.Profiles) > 0 && !slices.Equal(target.Profiles, s.Profiles):
			add("depends on %s, which isn't in every profile it runs in", dep)
		}
	}

	if s.Healthcheck == nil {
		add("needs a healthcheck, so that tests know when it is ready")
	} else {
		h := s.Healthcheck
		if len(h.Test) < 2 || (h.Test[0] != "CMD" && h.Test[0] != "CMD-SHELL") {
			add("healthcheck test must be a list starting with CMD or CMD-SHELL")
		}
		for _, v := range h.Test {
			if strings.Contains(v, "$") {
				add("healthcheck test uses $, which Compose would substitute from the shell")
			}
		}
		if h.StartPeriod == 0 || h.StartInterval == 0 || h.Interval == 0 || h.Timeout == 0 || h.Retries <= 0 {
			add("healthcheck needs start_period, start_interval, interval, timeout and retries")
		}
	}

	// testcontainers-go publishes every port an image exposes unless it is
	// told which ones to publish, and then binds them to every interface.
	// Listing them lets netboxtest bind each one to 127.0.0.1 instead, so a
	// service that listens on none still lists one.
	if len(s.Expose) == 0 && len(s.Ports) == 0 {
		add("must list its container ports under expose or ports, so netboxtest can keep them on 127.0.0.1")
	}
	for _, p := range s.Expose {
		if _, err := parsePort(p); err != nil {
			add("expose %q: %v", p, err)
		}
	}
	for _, p := range s.Ports {
		if _, err := containerPort(p); err != nil {
			add("ports %q: %v", p, err)
		}
	}
	return errs
}

// containerPorts returns the container ports to publish, as "8080/tcp".
// parseStack has checked them.
func (s service) containerPorts() []string {
	var ports []string
	for _, p := range s.Expose {
		n, _ := parsePort(p)
		ports = append(ports, strconv.Itoa(n)+"/tcp")
	}
	for _, p := range s.Ports {
		n, _ := containerPort(p)
		ports = append(ports, strconv.Itoa(n)+"/tcp")
	}
	return ports
}

// hostPortVariable is a host port taken from a variable with a default, such
// as ${NETBOX_PORT:-8000}. Tests choose their own host port, so they ignore it.
var hostPortVariable = regexp.MustCompile(`^\$\{[A-Z][A-Z0-9_]*:-[0-9]+\}$`)

// containerPort returns the container port of a "127.0.0.1:HOST:CONTAINER"
// mapping. The host port may use a variable, which itself contains a colon.
func containerPort(mapping string) (int, error) {
	hostIP, rest, ok := strings.Cut(mapping, ":")
	i := strings.LastIndex(rest, ":")
	if !ok || i < 0 {
		return 0, errors.New("must be 127.0.0.1:HOST_PORT:CONTAINER_PORT")
	}
	// Exactly 127.0.0.1, which is where tests publish and the Makefile connects.
	if hostIP != "127.0.0.1" {
		return 0, errors.New("must publish on 127.0.0.1 only")
	}
	// Empty or 0 lets Docker choose the host port.
	if hostPort := rest[:i]; hostPort != "" && hostPort != "0" && !hostPortVariable.MatchString(hostPort) {
		if _, err := parsePort(hostPort); err != nil {
			return 0, fmt.Errorf("host port %q must be a number, empty, or ${VARIABLE:-default}", hostPort)
		}
	}
	return parsePort(rest[i+1:])
}

func isDigest(s string) bool {
	return len(s) == 64 && strings.Trim(s, "0123456789abcdef") == ""
}

// isPinned reports whether image names a tag and pins a digest, as
// name:tag@sha256:<digest>.
func isPinned(image string) bool {
	ref, digest, ok := strings.Cut(image, "@sha256:")
	return ok && isDigest(digest) && strings.Contains(ref[strings.LastIndex(ref, "/")+1:], ":")
}

func profileNames() string {
	names := make([]string, len(profiles))
	for i, p := range profiles {
		names[i] = string(p)
	}
	return strings.Join(names, ", ")
}

// contextName is a build context netboxtest can embed: a directory beside
// compose.yaml, named plainly.
var contextName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// dockerfileDirective is a Dockerfile parser directive, such as
// "# syntax=docker/dockerfile:1", which changes how the file is read.
var dockerfileDirective = regexp.MustCompile(`(?i)^#\s*(syntax|escape|check)\s*=`)

// dockerfileBase checks the Dockerfile of a build context and returns the
// image it builds on.
//
// Compose builds with BuildKit, and netboxtest with Docker's classic builder,
// through testcontainers-go. The Dockerfile may use only what both build the
// same way, from pinned inputs: one FROM, a pinned image, first; then RUN and
// COPY without flags or here-documents, which leaves out RUN --mount and <<,
// BuildKit's alone, and COPY --from, which would copy from an image that
// isn't pinned. No ARG,
// since neither passes build arguments, no ADD, which can fetch URLs, and no
// parser directives.
func dockerfileBase(contexts fs.FS, context string) (string, error) {
	if !contextName.MatchString(context) {
		return "", fmt.Errorf("context %q must be a directory beside compose.yaml, named in lower case letters, digits and hyphens", context)
	}
	data, err := fs.ReadFile(contexts, path.Join(context, "Dockerfile"))
	if err != nil {
		return "", fmt.Errorf("context %s: read its Dockerfile from what netboxtest embeds (see buildContexts): %w", context, err)
	}
	var base string
	var instruction strings.Builder
	start := 0
	for i, line := range strings.Split(string(data), "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "#") {
			if dockerfileDirective.MatchString(t) {
				return "", fmt.Errorf("the Dockerfile's line %d: parser directives aren't allowed", i+1)
			}
			continue
		}
		// Docker skips blank lines, within an instruction too.
		if t == "" {
			continue
		}
		if instruction.Len() == 0 {
			start = i + 1
		}
		if more, ok := strings.CutSuffix(t, "\\"); ok {
			instruction.WriteString(more + " ")
			continue
		}
		instruction.WriteString(t)
		fields := strings.Fields(instruction.String())
		instruction.Reset()
		if len(fields) == 0 {
			continue
		}
		keyword, args := strings.ToUpper(fields[0]), fields[1:]
		switch {
		case keyword == "FROM" && base != "":
			return "", fmt.Errorf("the Dockerfile's line %d: a second FROM; build on one image only", start)
		case keyword == "FROM":
			if len(args) != 1 || !isPinned(args[0]) {
				return "", fmt.Errorf("the Dockerfile's line %d: FROM must name one image, with a tag and a pinned digest, as name:tag@sha256:<digest>", start)
			}
			base = args[0]
		case keyword != "RUN" && keyword != "COPY":
			return "", fmt.Errorf("the Dockerfile's line %d: %s isn't allowed; netboxtest builds only FROM, RUN and COPY", start, keyword)
		case base == "":
			return "", fmt.Errorf("the Dockerfile's line %d: FROM must come first", start)
		case len(args) == 0:
			return "", fmt.Errorf("the Dockerfile's line %d: %s has nothing to do", start, keyword)
		case strings.HasPrefix(args[0], "--"):
			return "", fmt.Errorf("the Dockerfile's line %d: %s %s isn't allowed; netboxtest builds %s without flags", start, keyword, args[0], keyword)
		case slices.ContainsFunc(args, func(a string) bool { return strings.Contains(a, "<<") }):
			// BuildKit reads the lines after <<EOF as a here-document; the
			// classic builder reads them as instructions.
			return "", fmt.Errorf("the Dockerfile's line %d: here-documents (<<) aren't allowed; only BuildKit builds them", start)
		}
	}
	if instruction.Len() > 0 {
		return "", errors.New("the Dockerfile ends in the middle of an instruction")
	}
	if base == "" {
		return "", errors.New("the Dockerfile has no FROM")
	}
	return base, nil
}

func parsePort(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 65535 {
		return 0, fmt.Errorf("%q isn't a TCP port number", s)
	}
	return n, nil
}

// startOrder sorts services so that each comes after its dependencies,
// alphabetically where the order is free, and reports a dependency cycle.
func startOrder(services map[string]service) ([]string, error) {
	var order []string
	started := map[string]bool{}
	for len(order) < len(services) {
		progress := false
		for _, name := range slices.Sorted(maps.Keys(services)) {
			if started[name] {
				continue
			}
			ready := true
			for dep := range services[name].DependsOn {
				ready = ready && started[dep]
			}
			if ready {
				order = append(order, name)
				started[name] = true
				progress = true
			}
		}
		if !progress {
			var waiting []string
			for _, name := range slices.Sorted(maps.Keys(services)) {
				if !started[name] {
					waiting = append(waiting, name)
				}
			}
			return nil, fmt.Errorf("services %s depend on each other in a cycle", strings.Join(waiting, ", "))
		}
	}
	return order, nil
}
