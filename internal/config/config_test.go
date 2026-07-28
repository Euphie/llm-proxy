package config

import (
	"strings"
	"testing"
	"time"
)

func testProvider() providerYAML {
	return providerYAML{
		Upstream: "https://example.test/anthropic",
		OverloadRules: []ruleYAML{
			{Status: 529},
		},
	}
}

func TestResolveVisionDefaults(t *testing.T) {
	pc := testProvider()
	pc.Vision.Enabled = true

	got, err := resolve("test", pc, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Vision.Model != "sonnet" ||
		got.Vision.MaxTokens != 2048 ||
		got.Vision.Timeout != 2*time.Minute ||
		got.Vision.MaxConcurrency != 4 ||
		got.Vision.CacheTTL != 30*time.Minute ||
		got.Vision.CacheMaxEntries != 512 {
		t.Fatalf("unexpected defaults: %+v", got.Vision)
	}
	if got.StatsPassword != "statspwd123456" {
		t.Fatalf("stats password=%q", got.StatsPassword)
	}
}

func TestResolveVisionDisabledByDefault(t *testing.T) {
	got, err := resolve("test", testProvider(), "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Vision.Enabled {
		t.Fatal("vision must be disabled by default")
	}
}

func TestResolveAllowsEmptyOverloadRules(t *testing.T) {
	pc := testProvider()
	pc.OverloadRules = nil

	got, err := resolve("test", pc, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.OverloadRules) != 0 {
		t.Fatalf("overload rules=%v", got.OverloadRules)
	}
}

func TestResolveVisionRequiresAnthropic(t *testing.T) {
	pc := testProvider()
	pc.Protocol = "openai"
	pc.Vision.Enabled = true

	_, err := resolve("test", pc, "", "", "")
	if err == nil || !strings.Contains(err.Error(), "vision requires protocol anthropic") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveVisionRejectsZeroValues(t *testing.T) {
	zero := 0
	zeroDuration := yamlDuration{}
	tests := []struct {
		name  string
		set   func(*visionYAML)
		field string
	}{
		{"max tokens", func(v *visionYAML) { v.MaxTokens = &zero }, "max_tokens"},
		{"timeout", func(v *visionYAML) { v.Timeout = &zeroDuration }, "timeout"},
		{"max concurrency", func(v *visionYAML) { v.MaxConcurrency = &zero }, "max_concurrency"},
		{"cache ttl", func(v *visionYAML) { v.CacheTTL = &zeroDuration }, "cache_ttl"},
		{"cache max entries", func(v *visionYAML) { v.CacheMaxEntries = &zero }, "cache_max_entries"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pc := testProvider()
			pc.Vision.Enabled = true
			tt.set(&pc.Vision)

			_, err := resolve("test", pc, "", "", "")
			if err == nil || !strings.Contains(err.Error(), "vision."+tt.field) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestResolveVisionExplicitValues(t *testing.T) {
	maxTokens := 4096
	timeout := yamlDuration{Duration: 45 * time.Second}
	maxConcurrency := 8
	cacheTTL := yamlDuration{Duration: time.Hour}
	cacheMaxEntries := 128
	pc := testProvider()
	pc.Vision = visionYAML{
		Enabled:         true,
		Model:           "claude-vision",
		MaxTokens:       &maxTokens,
		Timeout:         &timeout,
		MaxConcurrency:  &maxConcurrency,
		CacheTTL:        &cacheTTL,
		CacheMaxEntries: &cacheMaxEntries,
		Prompt:          "describe the image",
	}

	got, err := resolve("test", pc, "", "", "custom-stats-password")
	if err != nil {
		t.Fatal(err)
	}
	want := VisionConfig{
		Enabled:         true,
		Model:           "claude-vision",
		MaxTokens:       maxTokens,
		Timeout:         timeout.Duration,
		MaxConcurrency:  maxConcurrency,
		CacheTTL:        cacheTTL.Duration,
		CacheMaxEntries: cacheMaxEntries,
		Prompt:          "describe the image",
	}
	if got.Vision != want {
		t.Fatalf("vision = %+v, want %+v", got.Vision, want)
	}
	if got.StatsPassword != "custom-stats-password" {
		t.Fatalf("stats password=%q", got.StatsPassword)
	}
}
