package main

import (
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"anthropic-proxy/internal/config"
	"anthropic-proxy/internal/proxy"
)

func main() {
	configFile := flag.String("config", os.Getenv("CONFIG_FILE"), "YAML config file path")
	flag.Parse()

	if *configFile == "" {
		slog.Error("config file is required: use -config <path> or set CONFIG_FILE env var")
		os.Exit(1)
	}

	cfg, err := config.Load(*configFile)
	if err != nil {
		slog.Error("configuration error", "err", err)
		os.Exit(1)
	}

	slog.Info("anthropic-proxy starting",
		"provider", cfg.ProviderName,
		"listen", cfg.ListenAddr,
		"upstream", cfg.Upstream,
		"overload_rules", fmtRules(cfg),
	)

	client := &http.Client{Timeout: 10 * time.Minute}
	mux := http.NewServeMux()
	mux.Handle("/", proxy.New(cfg, client))

	if err := http.ListenAndServe(cfg.ListenAddr, mux); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func fmtRules(cfg *config.Config) string {
	parts := make([]string, len(cfg.OverloadRules))
	for i, r := range cfg.OverloadRules {
		if r.BodyContains != "" {
			parts[i] = fmt.Sprintf("%d+%q(max=%d,delay=%v,jitter=%v)",
				r.Status, r.BodyContains, r.MaxRetries, r.RetryDelay, r.RetryJitter)
		} else {
			parts[i] = fmt.Sprintf("%d(max=%d,delay=%v,jitter=%v)",
				r.Status, r.MaxRetries, r.RetryDelay, r.RetryJitter)
		}
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
