package main

import (
	"log/slog"
	"testing"
)

// Break caught: ignoring LISTEN or DATA_DIR when command-line flags are omitted.
func TestParseRuntimeOptionsUsesEnvironmentDefaults(t *testing.T) {
	t.Setenv("LISTEN", "127.0.0.1:9080")
	t.Setenv("DATA_DIR", "/runtime/data")
	t.Setenv("LOG_LEVEL", "debug")

	options, err := parseRuntimeOptions(nil)
	if err != nil {
		t.Fatal(err)
	}
	if options.listen != "127.0.0.1:9080" || options.dataDir != "/runtime/data" ||
		options.logLevel != slog.LevelDebug {
		t.Fatalf("options=%+v", options)
	}
}

// Break caught: allowing blank environment values to erase the runnable local defaults.
func TestParseRuntimeOptionsUsesBuiltInDefaultsForBlankEnvironment(t *testing.T) {
	t.Setenv("LISTEN", "")
	t.Setenv("DATA_DIR", "")
	t.Setenv("LOG_LEVEL", "")

	options, err := parseRuntimeOptions(nil)
	if err != nil {
		t.Fatal(err)
	}
	if options.listen != ":8080" || options.dataDir != "./data" ||
		options.logLevel != slog.LevelInfo {
		t.Fatalf("options=%+v", options)
	}
}

// Break caught: defining flags without applying them over the environment-backed defaults.
func TestParseRuntimeOptionsFlagsOverrideEnvironment(t *testing.T) {
	t.Setenv("LISTEN", "127.0.0.1:9080")
	t.Setenv("DATA_DIR", "/runtime/data")
	t.Setenv("LOG_LEVEL", "info")

	options, err := parseRuntimeOptions([]string{
		"-listen", "127.0.0.1:10080",
		"-data-dir", "/flag/data",
		"-log-level", "warn",
	})
	if err != nil {
		t.Fatal(err)
	}
	if options.listen != "127.0.0.1:10080" || options.dataDir != "/flag/data" ||
		options.logLevel != slog.LevelWarn {
		t.Fatalf("options=%+v", options)
	}
}

func TestParseRuntimeOptionsRejectsInvalidLogLevel(t *testing.T) {
	t.Setenv("LOG_LEVEL", "verbose")
	if _, err := parseRuntimeOptions(nil); err == nil {
		t.Fatal("parseRuntimeOptions accepted an invalid log level")
	}
}
