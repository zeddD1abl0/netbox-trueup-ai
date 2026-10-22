package version

import (
	"runtime/debug"
	"testing"
)

func TestResolve(t *testing.T) {
	t.Parallel()

	withMain := func(v string) *debug.BuildInfo {
		return &debug.BuildInfo{Main: debug.Module{Version: v}}
	}

	tests := []struct {
		name    string
		stamped string
		info    *debug.BuildInfo
		want    string
	}{
		{name: "stamped version wins", stamped: "v1.2.3", info: withMain("v0.0.0-20261022000000-abcdef123456"), want: "v1.2.3"},
		{name: "module version from VCS", info: withMain("v0.0.0-20261022000000-abcdef123456"), want: "v0.0.0-20261022000000-abcdef123456"},
		{name: "devel build", info: withMain("(devel)"), want: "dev"},
		{name: "empty module version", info: withMain(""), want: "dev"},
		{name: "no build info", info: nil, want: "dev"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := resolve(tt.stamped, tt.info); got != tt.want {
				t.Errorf("resolve(%q, info) = %q, want %q", tt.stamped, got, tt.want)
			}
		})
	}
}
