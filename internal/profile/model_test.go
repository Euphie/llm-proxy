package profile

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRecordResolveAppliesRuntimeValues(t *testing.T) {
	record := Record{
		ID: 7, Slug: "coding", DisplayName: "Coding", Enabled: true,
		Config: NewConfig(ProtocolAnthropic, "https://example.test/anthropic"),
	}
	runtime, err := record.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if runtime.ID != 7 || runtime.Slug != "coding" {
		t.Fatalf("identity=%+v", runtime)
	}
	if runtime.Vision.Model != "sonnet" ||
		runtime.Vision.MaxTokens != 2048 ||
		runtime.Vision.Timeout != 2*time.Minute ||
		runtime.Vision.CacheTTL != 30*time.Minute {
		t.Fatalf("vision defaults=%+v", runtime.Vision)
	}
}

func TestRecordResolveRejectsReservedSlug(t *testing.T) {
	record := Record{
		Slug: "v1", DisplayName: "Reserved", Enabled: true,
		Config: NewConfig(ProtocolAnthropic, "https://example.test"),
	}
	if _, err := record.Resolve(); !errors.Is(err, ErrInvalidSlug) {
		t.Fatalf("error=%v", err)
	}
}

func TestRecordResolveValidation(t *testing.T) {
	valid := Record{
		Slug: "coding", DisplayName: "Coding", Enabled: true,
		Config: NewConfig(ProtocolAnthropic, "https://example.test"),
	}

	tests := []struct {
		name   string
		mutate func(*Record)
		want   error
	}{
		{
			name: "leading underscore slug",
			mutate: func(r *Record) {
				r.Slug = "_admin"
			},
			want: ErrInvalidSlug,
		},
		{
			name: "uppercase slug",
			mutate: func(r *Record) {
				r.Slug = "Coding"
			},
			want: ErrInvalidSlug,
		},
		{
			name: "space in slug",
			mutate: func(r *Record) {
				r.Slug = "coding team"
			},
			want: ErrInvalidSlug,
		},
		{
			name: "64 character slug",
			mutate: func(r *Record) {
				r.Slug = strings.Repeat("a", 64)
			},
			want: ErrInvalidSlug,
		},
		{
			name: "unsupported protocol",
			mutate: func(r *Record) {
				r.Config.Protocol = "other"
			},
			want: ErrInvalidProtocol,
		},
		{
			name: "upstream credentials",
			mutate: func(r *Record) {
				r.Config.Upstream = "https://user:password@example.test"
			},
			want: ErrInvalidConfig,
		},
		{
			name: "upstream query",
			mutate: func(r *Record) {
				r.Config.Upstream = "https://example.test?key=value"
			},
			want: ErrInvalidConfig,
		},
		{
			name: "upstream fragment",
			mutate: func(r *Record) {
				r.Config.Upstream = "https://example.test#fragment"
			},
			want: ErrInvalidConfig,
		},
		{
			name: "openai vision",
			mutate: func(r *Record) {
				r.Config.Protocol = ProtocolOpenAI
				r.Config.Vision.Enabled = true
			},
			want: ErrInvalidConfig,
		},
		{
			name: "zero max tokens",
			mutate: func(r *Record) {
				r.Config.Vision.Enabled = true
				r.Config.Vision.MaxTokens = 0
			},
			want: ErrInvalidConfig,
		},
		{
			name: "zero max concurrency",
			mutate: func(r *Record) {
				r.Config.Vision.Enabled = true
				r.Config.Vision.MaxConcurrency = 0
			},
			want: ErrInvalidConfig,
		},
		{
			name: "zero cache max entries",
			mutate: func(r *Record) {
				r.Config.Vision.Enabled = true
				r.Config.Vision.CacheMaxEntries = 0
			},
			want: ErrInvalidConfig,
		},
		{
			name: "invalid vision timeout",
			mutate: func(r *Record) {
				r.Config.Vision.Timeout = "invalid"
			},
			want: ErrInvalidConfig,
		},
		{
			name: "invalid cache ttl",
			mutate: func(r *Record) {
				r.Config.Vision.CacheTTL = "invalid"
			},
			want: ErrInvalidConfig,
		},
		{
			name: "zero vision timeout",
			mutate: func(r *Record) {
				r.Config.Vision.Enabled = true
				r.Config.Vision.Timeout = "0s"
			},
			want: ErrInvalidConfig,
		},
		{
			name: "negative vision timeout",
			mutate: func(r *Record) {
				r.Config.Vision.Enabled = true
				r.Config.Vision.Timeout = "-1s"
			},
			want: ErrInvalidConfig,
		},
		{
			name: "zero cache ttl",
			mutate: func(r *Record) {
				r.Config.Vision.Enabled = true
				r.Config.Vision.CacheTTL = "0s"
			},
			want: ErrInvalidConfig,
		},
		{
			name: "negative cache ttl",
			mutate: func(r *Record) {
				r.Config.Vision.Enabled = true
				r.Config.Vision.CacheTTL = "-1s"
			},
			want: ErrInvalidConfig,
		},
		{
			name: "invalid retry delay",
			mutate: func(r *Record) {
				r.Config.OverloadRules = []RetryRule{{Delay: "invalid"}}
			},
			want: ErrInvalidConfig,
		},
		{
			name: "invalid retry jitter",
			mutate: func(r *Record) {
				r.Config.OverloadRules = []RetryRule{{Jitter: "invalid"}}
			},
			want: ErrInvalidConfig,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := valid
			test.mutate(&record)
			if _, err := record.Resolve(); !errors.Is(err, test.want) {
				t.Fatalf("error=%v, want %v", err, test.want)
			}
		})
	}
}

func TestRecordResolveTrimsUpstreamTrailingSlashAndBuildsRetryRules(t *testing.T) {
	record := Record{
		Slug: "coding", DisplayName: "Coding", Enabled: true,
		Config: NewConfig(ProtocolAnthropic, "https://example.test/api/"),
	}
	record.Config.OverloadRules = []RetryRule{{
		Status: 529, BodyContains: "overloaded", MaxRetries: 2,
		Delay: "1s", Jitter: "100ms",
	}}

	runtime, err := record.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Upstream != "https://example.test/api" {
		t.Fatalf("upstream=%q", runtime.Upstream)
	}
	if len(runtime.OverloadRules) != 1 {
		t.Fatalf("rules=%+v", runtime.OverloadRules)
	}
	rule := runtime.OverloadRules[0]
	if rule.Status != 529 || rule.BodyContains != "overloaded" || rule.MaxRetries != 2 ||
		rule.RetryDelay != time.Second || rule.RetryJitter != 100*time.Millisecond {
		t.Fatalf("rule=%+v", rule)
	}
}
