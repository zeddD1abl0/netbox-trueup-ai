package netboxtest

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

//go:embed compose.yaml
var composeFile []byte

// netboxService is the service in compose.yaml that runs NetBox.
const netboxService = "netbox"

// stack is the part of the Compose file format that netboxtest can run the
// same way Docker Compose does. Decoding rejects every other key, so
// compose.yaml can't use a feature that the tests would silently ignore.
type stack struct {
	Name     string             `yaml:"name"`
	Services map[string]service `yaml:"services"`
}

type service struct {
	Image       string                `yaml:"image"`
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

// parseStack decodes and checks a Compose file. It returns the stack and its
// service names in start order: every service after the services it depends on.
func parseStack(data []byte) (*stack, []string, error) {
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
		for _, err := range st.Services[name].check(st.Services) {
			problems = append(problems, fmt.Errorf("service %s: %w", name, err))
		}
	}
	if nb, ok := st.Services[netboxService]; !ok {
		problems = append(problems, fmt.Errorf("no %s service", netboxService))
	} else {
		if len(nb.Ports) != 1 {
			problems = append(problems, fmt.Errorf("service %s: needs exactly one entry in ports, for NetBox's web server", netboxService))
		}
		for _, key := range []string{"SUPERUSER_NAME", "SUPERUSER_PASSWORD"} {
			if nb.Environment[key] == "" {
				problems = append(problems, fmt.Errorf("service %s: environment needs %s, to provision tokens with", netboxService, key))
			}
		}
	}
	if len(problems) > 0 {
		return nil, nil, errors.Join(problems...)
	}

	order, err := startOrder(st.Services)
	if err != nil {
		return nil, nil, err
	}
	return &st, order, nil
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

func (s service) check(all map[string]service) []error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	// Dependabot needs the tag to propose updates, and the digest makes the
	// pull reproducible.
	if ref, digest, ok := strings.Cut(s.Image, "@sha256:"); !ok || !isDigest(digest) || !strings.Contains(ref[strings.LastIndex(ref, "/")+1:], ":") {
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
	// Listing them lets netboxtest bind each one to 127.0.0.1 instead.
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
