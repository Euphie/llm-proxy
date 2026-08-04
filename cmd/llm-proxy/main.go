package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/Euphie/llm-proxy/internal/app"
)

type runtimeOptions struct {
	listen   string
	dataDir  string
	logLevel slog.Level
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("llm-proxy stopped", "err", err)
		os.Exit(1)
	}
}

func run(args []string) (err error) {
	options, err := parseRuntimeOptions(args)
	if err != nil {
		return err
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: options.logLevel,
	})))
	application, err := app.New(app.Options{DataDir: options.dataDir})
	if err != nil {
		return fmt.Errorf("initialize application: %w", err)
	}
	defer func() {
		if closeErr := application.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close application: %w", closeErr))
		}
	}()

	slog.Info(
		"llm-proxy starting",
		"listen", options.listen,
		"data_dir", options.dataDir,
		"log_level", options.logLevel,
	)
	if err := http.ListenAndServe(options.listen, application.Handler()); err != nil {
		return fmt.Errorf("serve HTTP: %w", err)
	}
	return nil
}

func parseRuntimeOptions(args []string) (runtimeOptions, error) {
	listen := envOrDefault("LISTEN", ":8080")
	dataDir := envOrDefault("DATA_DIR", "./data")
	logLevelText := envOrDefault("LOG_LEVEL", "info")
	options := runtimeOptions{listen: listen, dataDir: dataDir}

	flags := flag.NewFlagSet("llm-proxy", flag.ContinueOnError)
	flags.StringVar(&options.listen, "listen", listen, "HTTP listen address")
	flags.StringVar(&options.dataDir, "data-dir", dataDir, "runtime data directory")
	flags.StringVar(&logLevelText, "log-level", logLevelText, "log level: debug, info, warn, or error")
	if err := flags.Parse(args); err != nil {
		return runtimeOptions{}, err
	}
	if flags.NArg() != 0 {
		return runtimeOptions{}, fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if err := options.logLevel.UnmarshalText([]byte(logLevelText)); err != nil {
		return runtimeOptions{}, fmt.Errorf("invalid log level %q", logLevelText)
	}
	return options, nil
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
