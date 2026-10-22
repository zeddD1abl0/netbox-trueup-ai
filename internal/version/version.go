// Package version reports which build of netbox-trueup-ai is running.
package version

import "runtime/debug"

// version is stamped by release builds:
//
//	go build -ldflags "-X github.com/zeddD1abl0/netbox-trueup-ai/internal/version.version=v1.2.3"
var version string

// String returns the stamped release version. Without one, it falls back to
// the module version the Go toolchain recorded from version control, and
// finally to "dev".
func String() string {
	info, _ := debug.ReadBuildInfo()
	return resolve(version, info)
}

func resolve(stamped string, info *debug.BuildInfo) string {
	if stamped != "" {
		return stamped
	}
	if info != nil && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return "dev"
}
