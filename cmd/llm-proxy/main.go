package main

import (
	"crypto/subtle"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Euphie/llm-proxy/internal/config"
	"github.com/Euphie/llm-proxy/internal/proxy"
	"github.com/Euphie/llm-proxy/internal/stats"
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

	slog.Info("llm-proxy starting",
		"provider", cfg.ProviderName,
		"listen", cfg.ListenAddr,
		"upstream", cfg.Upstream,
		"overload_rules", fmtRules(cfg),
	)

	// Initialize token usage stats (optional)
	var sdb *stats.DB
	if cfg.StatsDB != "" {
		sdb, err = stats.Open(cfg.StatsDB)
		if err != nil {
			slog.Error("stats: failed to open db", "path", cfg.StatsDB, "err", err)
			os.Exit(1)
		}
		defer sdb.Close()
		slog.Info("stats enabled", "db", cfg.StatsDB, "endpoint", "/stats")
	}

	client := &http.Client{Timeout: 10 * time.Minute}
	mux := http.NewServeMux()
	if sdb != nil {
		mux.Handle("/stats/data", statsBasicAuth(cfg.StatsPassword, sdb.Handler()))
		mux.Handle("/stats", statsBasicAuth(cfg.StatsPassword, sdb.UIHandler()))
	}
	mux.Handle("/", proxy.New(cfg, client, sdb))

	if err := http.ListenAndServe(cfg.ListenAddr, mux); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func statsBasicAuth(password string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, suppliedPassword, ok := r.BasicAuth()
		validUsername := subtle.ConstantTimeCompare([]byte(username), []byte("admin")) == 1
		validPassword := subtle.ConstantTimeCompare([]byte(suppliedPassword), []byte(password)) == 1
		if !ok || !validUsername || !validPassword {
			w.Header().Set("WWW-Authenticate", `Basic realm="llm-proxy stats", charset="UTF-8"`)
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
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
