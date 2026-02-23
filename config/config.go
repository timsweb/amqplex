package config

import (
	"fmt"
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	ListenAddress          string
	ListenPort             int
	PoolIdleTimeout        int
	PoolMaxChannels        int
	MaxUpstreamConnections int // 0 = unlimited
	MaxClientConnections   int // 0 = unlimited
	UpstreamURL            string
	// Server TLS configuration (for accepting connections)
	TLSCert string
	TLSKey  string
	// Client TLS configuration (for connecting to upstream)
	TLSCACert     string
	TLSClientCert string
	TLSClientKey  string
	TLSSkipVerify bool
}

func LoadConfig(configPath string, envPrefix string) (*Config, error) {
	v := viper.New()

	// Set defaults
	v.SetDefault("listen.address", "0.0.0.0")
	v.SetDefault("listen.port", 5673)
	v.SetDefault("pool.idle_timeout", 5)
	v.SetDefault("pool.max_channels", 65535)
	v.SetDefault("pool.max_upstream_connections", 0)
	v.SetDefault("pool.max_client_connections", 0)

	// Set env prefix
	if envPrefix != "" {
		v.SetEnvPrefix(envPrefix)
		v.AutomaticEnv()
	}

	// Load config file if provided
	if configPath != "" {
		v.SetConfigFile(configPath)
		if err := v.ReadInConfig(); err != nil {
			return nil, fmt.Errorf("failed to read config: %w", err)
		}
	}

	// Map env vars to config keys
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	cfg := &Config{
		ListenAddress:          v.GetString("listen.address"),
		ListenPort:             v.GetInt("listen.port"),
		PoolIdleTimeout:        v.GetInt("pool.idle_timeout"),
		PoolMaxChannels:        v.GetInt("pool.max_channels"),
		MaxUpstreamConnections: v.GetInt("pool.max_upstream_connections"),
		MaxClientConnections:   v.GetInt("pool.max_client_connections"),
		UpstreamURL:            v.GetString("upstream.url"),
		// Server TLS fields
		TLSCert: v.GetString("tls.cert"),
		TLSKey:  v.GetString("tls.key"),
		// Client TLS fields
		TLSCACert:     v.GetString("tls.ca_cert"),
		TLSClientCert: v.GetString("tls.client_cert"),
		TLSClientKey:  v.GetString("tls.client_key"),
		TLSSkipVerify: v.GetBool("tls.skip_verify"),
	}

	return cfg, nil
}
