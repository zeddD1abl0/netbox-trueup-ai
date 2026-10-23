//go:build !windows

package config

const (
	defaultConfigFile = "/etc/netbox-trueup-ai/config.yaml"
	defaultDataDir    = "/var/lib/netbox-trueup-ai"
)
