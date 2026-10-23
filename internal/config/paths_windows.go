//go:build windows

package config

import (
	"os"
	"path/filepath"
)

var (
	defaultConfigFile = filepath.Join(programData(), "netbox-trueup-ai", "config.yaml")
	defaultDataDir    = filepath.Join(programData(), "netbox-trueup-ai")
)

func programData() string {
	if dir := os.Getenv("ProgramData"); dir != "" {
		return dir
	}
	return `C:\ProgramData`
}
