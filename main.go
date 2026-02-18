package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/tim/amqproxy/config"
	"github.com/tim/amqproxy/health"
	"github.com/tim/amqproxy/proxy"
)

func main() {
	configPath, upstreamURL, err := parseFlags(os.Args[1:])
	if err != nil {
		log.Fatalf("Error parsing flags: %v", err)
	}

	cfg, err := config.LoadConfig(configPath, "AMQP")
	if err != nil {
		log.Fatalf("Error loading config: %v", err)
	}

	// Override upstream URL if provided via command line
	if upstreamURL != "" {
		cfg.UpstreamURL = upstreamURL
	}

	p, err := proxy.NewProxy(cfg)
	if err != nil {
		log.Fatalf("Error creating proxy: %v", err)
	}

	// Start health check server
	go func() {
		healthAddr := fmt.Sprintf("%s:%d", cfg.ListenAddress, cfg.ListenPort+1)
		healthHandler := health.NewHealthHandler()
		log.Printf("Health check server listening on %s", healthAddr)
		log.Fatal(http.ListenAndServe(healthAddr, healthHandler))
	}()

	// Start AMQP proxy
	log.Printf("AMQP Proxy listening on %s", fmt.Sprintf("%s:%d", cfg.ListenAddress, cfg.ListenPort))
	log.Fatal(p.Start())
}

func parseFlags(args []string) (string, string, error) {
	flagSet := flag.NewFlagSet("amqproxy", flag.ContinueOnError)
	configPtr := flagSet.String("config", "", "Path to config file")

	err := flagSet.Parse(args)
	if err != nil {
		return "", "", err
	}

	remaining := flagSet.Args()
	if len(remaining) > 1 {
		return "", "", fmt.Errorf("too many arguments")
	}

	var upstreamURL string
	if len(remaining) == 1 {
		upstreamURL = remaining[0]
	}

	return *configPtr, upstreamURL, nil
}
