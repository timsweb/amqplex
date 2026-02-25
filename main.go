package main

import (
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/timsweb/amqplex/config"
	"github.com/timsweb/amqplex/health"
	"github.com/timsweb/amqplex/proxy"
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

	logger := buildLogger()
	p, err := proxy.NewProxy(cfg, logger)
	if err != nil {
		log.Fatalf("Error creating proxy: %v", err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	// stopDone is closed after Stop() completes its drain.
	stopDone := make(chan struct{})

	// On signal, call Stop() in a goroutine. Stop() closes the listener (making
	// p.Start() return) and then drains active connections. We wait on stopDone
	// after Start() returns so the process does not exit before drain completes.
	go func() {
		<-sigCh
		if err := p.Stop(); err != nil {
			log.Printf("Stop error: %v", err)
		}
		close(stopDone)
	}()

	// Start health/metrics server
	go func() {
		addr := fmt.Sprintf("%s:%d", cfg.ListenAddress, cfg.ListenPort+1)
		mux := http.NewServeMux()
		mux.Handle("/", health.NewHealthHandler())
		mux.Handle("/metrics", p.MetricsHandler())
		log.Printf("Health/metrics server listening on %s", addr)
		log.Fatal(http.ListenAndServe(addr, mux))
	}()

	// Start AMQP proxy — blocks until listener is closed (by Stop()).
	log.Printf("AMQP Proxy listening on %s", fmt.Sprintf("%s:%d", cfg.ListenAddress, cfg.ListenPort))
	if err := p.Start(); err != nil {
		log.Fatal(err)
	}

	// p.Start() returned because the listener was closed. Wait for Stop()'s
	// drain to complete before the process exits.
	<-stopDone
}

func buildLogger() *slog.Logger {
	levelStr := strings.ToLower(os.Getenv("LOG_LEVEL"))
	var level slog.Level
	switch levelStr {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if strings.ToLower(os.Getenv("LOG_FORMAT")) == "json" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	return slog.New(handler)
}

func parseFlags(args []string) (string, string, error) {
	flagSet := flag.NewFlagSet("amqplex", flag.ContinueOnError)
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
