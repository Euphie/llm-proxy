package main

import "testing"

// Break caught: ignoring LISTEN or DATA_DIR when command-line flags are omitted.
func TestParseRuntimeOptionsUsesEnvironmentDefaults(t *testing.T) {
	t.Setenv("LISTEN", "127.0.0.1:9080")
	t.Setenv("DATA_DIR", "/runtime/data")

	options, err := parseRuntimeOptions(nil)
	if err != nil {
		t.Fatal(err)
	}
	if options.listen != "127.0.0.1:9080" || options.dataDir != "/runtime/data" {
		t.Fatalf("options=%+v", options)
	}
}

// Break caught: allowing blank environment values to erase the runnable local defaults.
func TestParseRuntimeOptionsUsesBuiltInDefaultsForBlankEnvironment(t *testing.T) {
	t.Setenv("LISTEN", "")
	t.Setenv("DATA_DIR", "")

	options, err := parseRuntimeOptions(nil)
	if err != nil {
		t.Fatal(err)
	}
	if options.listen != ":8080" || options.dataDir != "./data" {
		t.Fatalf("options=%+v", options)
	}
}

// Break caught: defining flags without applying them over the environment-backed defaults.
func TestParseRuntimeOptionsFlagsOverrideEnvironment(t *testing.T) {
	t.Setenv("LISTEN", "127.0.0.1:9080")
	t.Setenv("DATA_DIR", "/runtime/data")

	options, err := parseRuntimeOptions([]string{
		"-listen", "127.0.0.1:10080",
		"-data-dir", "/flag/data",
	})
	if err != nil {
		t.Fatal(err)
	}
	if options.listen != "127.0.0.1:10080" || options.dataDir != "/flag/data" {
		t.Fatalf("options=%+v", options)
	}
}
