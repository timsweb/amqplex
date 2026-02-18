package main

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func TestParseFlags(t *testing.T) {
	configPath, upstreamURL, err := parseFlags([]string{"--config", "test.toml", "amqps://rabbitmq:5671"})
	assert.NoError(t, err)
	assert.Equal(t, "test.toml", configPath)
	assert.Equal(t, "amqps://rabbitmq:5671", upstreamURL)
}
