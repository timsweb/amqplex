package config

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	config, err := LoadConfig("testdata/config.toml", "AMQP")
	assert.NoError(t, err)
	assert.Equal(t, "0.0.0.0", config.ListenAddress)
	assert.Equal(t, 5673, config.ListenPort)
	assert.Equal(t, 5, config.PoolIdleTimeout)
	assert.Equal(t, "amqps://rabbitmq:5671", config.UpstreamURL)
	assert.Equal(t, "/path/to/ca.crt", config.TLSCACert)
}
