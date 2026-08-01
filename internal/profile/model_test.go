package profile

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

const browserMaxSafeInteger int64 = 9_007_199_254_740_991

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

func TestRecordResolveLegacyUnlistedModelPolicyDefaultsToBypass(t *testing.T) {
	tests := []struct {
		name   string
		policy string
	}{
		{name: "missing stored policy"},
		{name: "empty stored policy", policy: `,"unlisted_model_policy":""`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := `{
				"version":1,
				"protocol":"anthropic",
				"upstream":"https://example.test",
				"vision":{
					"enabled":false,
					"model":"sonnet"` + tt.policy + `,
					"max_tokens":2048,
					"timeout":"2m",
					"max_concurrency":4,
					"cache_ttl":"30m",
					"cache_max_entries":512
				},
				"overload_rules":[]
			}`
			var config Config
			if err := json.Unmarshal([]byte(raw), &config); err != nil {
				t.Fatal(err)
			}
			runtime, err := (Record{
				Slug:        "legacy",
				DisplayName: "Legacy",
				Enabled:     true,
				Config:      config,
			}).Resolve()
			if err != nil {
				t.Fatal(err)
			}
			if len(runtime.Models) != 0 {
				t.Fatalf("models=%+v, want empty legacy catalog", runtime.Models)
			}
			if runtime.Vision.UnlistedModelPolicy != UnlistedModelBypass {
				t.Fatalf(
					"policy=%q, want %q",
					runtime.Vision.UnlistedModelPolicy,
					UnlistedModelBypass,
				)
			}
		})
	}
}

func TestRecordResolveAcceptsBrowserSafeIntegerLimitBoundary(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("browser safe integer boundary is not representable as int")
	}
	limit := int(browserMaxSafeInteger)
	supportsVision := true
	record := Record{
		Slug: "coding", DisplayName: "Coding", Enabled: true,
		Config: NewConfig(ProtocolAnthropic, "https://example.test"),
	}
	record.Config.Models = []ModelCapabilityConfig{
		{ID: "context-boundary", ContextWindow: &limit, SupportsVision: &supportsVision},
		{ID: "output-boundary", MaxOutputTokens: &limit, SupportsVision: &supportsVision},
	}

	if _, err := record.Resolve(); err != nil {
		t.Fatalf("Resolve() error=%v, want browser-safe boundary accepted", err)
	}
}

func TestRecordResolveRejectsLimitsBeyondBrowserSafeInteger(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("values beyond the browser safe integer boundary are not representable as int")
	}
	tooLarge := int(browserMaxSafeInteger + 1)
	supportsVision := true
	tests := []struct {
		name  string
		model ModelCapabilityConfig
	}{
		{
			name: "context window",
			model: ModelCapabilityConfig{
				ID: "context-too-large", ContextWindow: &tooLarge, SupportsVision: &supportsVision,
			},
		},
		{
			name: "max output tokens",
			model: ModelCapabilityConfig{
				ID: "output-too-large", MaxOutputTokens: &tooLarge, SupportsVision: &supportsVision,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record := Record{
				Slug: "coding", DisplayName: "Coding", Enabled: true,
				Config: NewConfig(ProtocolAnthropic, "https://example.test"),
			}
			record.Config.Models = []ModelCapabilityConfig{tt.model}
			if _, err := record.Resolve(); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("Resolve() error=%v, want ErrInvalidConfig", err)
			}
		})
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
			name: "empty model capability ID",
			mutate: func(r *Record) {
				supportsVision := true
				r.Config.Models = []ModelCapabilityConfig{{SupportsVision: &supportsVision}}
			},
			want: ErrInvalidConfig,
		},
		{
			name: "model capability ID has surrounding whitespace",
			mutate: func(r *Record) {
				supportsVision := true
				r.Config.Models = []ModelCapabilityConfig{{ID: " glm-5 ", SupportsVision: &supportsVision}}
			},
			want: ErrInvalidConfig,
		},
		{
			name: "duplicate exact model capability IDs",
			mutate: func(r *Record) {
				supportsVision := true
				r.Config.Models = []ModelCapabilityConfig{
					{ID: "glm-5", SupportsVision: &supportsVision},
					{ID: "glm-5", SupportsVision: &supportsVision},
				}
			},
			want: ErrInvalidConfig,
		},
		{
			name: "absent model supports vision",
			mutate: func(r *Record) {
				r.Config.Models = []ModelCapabilityConfig{{ID: "glm-5"}}
			},
			want: ErrInvalidConfig,
		},
		{
			name: "zero model context window",
			mutate: func(r *Record) {
				supportsVision := true
				zero := 0
				r.Config.Models = []ModelCapabilityConfig{{ID: "glm-5", ContextWindow: &zero, SupportsVision: &supportsVision}}
			},
			want: ErrInvalidConfig,
		},
		{
			name: "negative model context window",
			mutate: func(r *Record) {
				supportsVision := true
				negative := -1
				r.Config.Models = []ModelCapabilityConfig{{ID: "glm-5", ContextWindow: &negative, SupportsVision: &supportsVision}}
			},
			want: ErrInvalidConfig,
		},
		{
			name: "zero model max output tokens",
			mutate: func(r *Record) {
				supportsVision := true
				zero := 0
				r.Config.Models = []ModelCapabilityConfig{{ID: "glm-5", MaxOutputTokens: &zero, SupportsVision: &supportsVision}}
			},
			want: ErrInvalidConfig,
		},
		{
			name: "negative model max output tokens",
			mutate: func(r *Record) {
				supportsVision := true
				negative := -1
				r.Config.Models = []ModelCapabilityConfig{{ID: "glm-5", MaxOutputTokens: &negative, SupportsVision: &supportsVision}}
			},
			want: ErrInvalidConfig,
		},
		{
			name: "model output equals context window",
			mutate: func(r *Record) {
				supportsVision := true
				limit := 32
				r.Config.Models = []ModelCapabilityConfig{{ID: "glm-5", ContextWindow: &limit, MaxOutputTokens: &limit, SupportsVision: &supportsVision}}
			},
			want: ErrInvalidConfig,
		},
		{
			name: "model output exceeds context window",
			mutate: func(r *Record) {
				supportsVision := true
				contextWindow := 32
				maxOutputTokens := 33
				r.Config.Models = []ModelCapabilityConfig{{ID: "glm-5", ContextWindow: &contextWindow, MaxOutputTokens: &maxOutputTokens, SupportsVision: &supportsVision}}
			},
			want: ErrInvalidConfig,
		},
		{
			name: "unsupported unlisted model policy",
			mutate: func(r *Record) {
				r.Config.Vision.UnlistedModelPolicy = "other"
			},
			want: ErrInvalidConfig,
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
			name: "empty enabled vision model",
			mutate: func(r *Record) {
				r.Config.Vision.Enabled = true
				r.Config.Vision.Model = " \t"
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

func TestRecordResolveBuildsModelCatalog(t *testing.T) {
	withoutLimits := false
	withLimits := true
	contextWindow := 128000
	maxOutputTokens := 8192
	record := Record{
		Slug: "coding", DisplayName: "Coding", Enabled: true,
		Config: NewConfig(ProtocolAnthropic, "https://example.test"),
	}
	record.Config.Models = []ModelCapabilityConfig{
		{ID: "GLM-5", SupportsVision: &withoutLimits},
		{ID: "glm-5", ContextWindow: &contextWindow, MaxOutputTokens: &maxOutputTokens, SupportsVision: &withLimits},
	}

	runtime, err := record.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if len(runtime.Models) != 2 {
		t.Fatalf("models=%+v", runtime.Models)
	}
	upper, ok := runtime.Models.Lookup("GLM-5")
	if !ok || upper.HasContextWindow || upper.HasMaxOutputTokens || upper.SupportsVision {
		t.Fatalf("upper=%+v ok=%t", upper, ok)
	}
	lower, ok := runtime.Models.Lookup("glm-5")
	if !ok || !lower.HasContextWindow || lower.ContextWindow != contextWindow ||
		!lower.HasMaxOutputTokens || lower.MaxOutputTokens != maxOutputTokens || !lower.SupportsVision {
		t.Fatalf("lower=%+v ok=%t", lower, ok)
	}
}

func TestRecordResolveTrimsEnabledVisionModelForRuntime(t *testing.T) {
	record := Record{
		Slug: "coding", DisplayName: "Coding", Enabled: true,
		Config: NewConfig(ProtocolAnthropic, "https://example.test"),
	}
	record.Config.Vision.Enabled = true
	record.Config.Vision.Model = "  vision-model\t"

	runtime, err := record.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Vision.Model != "vision-model" || record.Config.Vision.Model != "  vision-model\t" {
		t.Fatalf("runtime=%q config=%q", runtime.Vision.Model, record.Config.Vision.Model)
	}
}

func TestRecordResolveAllowsOpenAIVision(t *testing.T) {
	record := Record{
		Slug: "openai", DisplayName: "OpenAI", Enabled: true,
		Config: NewConfig(ProtocolOpenAI, "https://example.test"),
	}
	record.Config.Vision.Enabled = true

	runtime, err := record.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Protocol != ProtocolOpenAI || !runtime.Vision.Enabled {
		t.Fatalf("runtime=%+v", runtime)
	}
}

func TestVisionTransportDefaultsAndValidation(t *testing.T) {
	tests := []struct {
		name      string
		protocol  Protocol
		transport VisionTransport
		want      VisionTransport
		wantErr   bool
	}{
		{name: "anthropic default", protocol: ProtocolAnthropic, want: VisionTransportAnthropicMessages},
		{name: "openai default", protocol: ProtocolOpenAI, want: VisionTransportOpenAIChatCompletions},
		{
			name: "openai responses", protocol: ProtocolOpenAI,
			transport: VisionTransportOpenAIResponses, want: VisionTransportOpenAIResponses,
		},
		{
			name: "anthropic rejects openai", protocol: ProtocolAnthropic,
			transport: VisionTransportOpenAIChatCompletions, wantErr: true,
		},
		{
			name: "openai rejects anthropic", protocol: ProtocolOpenAI,
			transport: VisionTransportAnthropicMessages, wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := Record{
				Slug: "vision", DisplayName: "Vision", Enabled: true,
				Config: NewConfig(test.protocol, "https://example.test/v1"),
			}
			record.Config.Vision.Transport = test.transport
			runtime, err := record.Resolve()
			if test.wantErr {
				if !errors.Is(err, ErrInvalidConfig) {
					t.Fatalf("error=%v, want invalid config", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if runtime.Vision.Transport != test.want {
				t.Fatalf("transport=%q, want %q", runtime.Vision.Transport, test.want)
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

func TestRecordResolveBuildsOrderedModelTargets(t *testing.T) {
	supportsVision := true
	record := Record{
		Slug: "coding", DisplayName: "Coding", Enabled: true,
		Config: NewConfig(ProtocolAnthropic, "https://primary.example/v1/"),
	}
	record.Config.Models = []ModelCapabilityConfig{
		{ID: "fast", SupportsVision: &supportsVision},
		{ID: "strong", SupportsVision: &supportsVision},
	}
	record.Config.Targets = []TargetConfig{
		{ID: "region_b", Upstream: "https://region-b.example/v1/", Models: []string{"fast", "strong"}},
		{ID: "strong_backup", Upstream: "https://strong.example/v1", Models: []string{"strong"}},
	}

	runtime, err := record.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	fast := runtime.RoutingTargets("fast")
	if len(fast) != 2 || fast[0].ID != PrimaryTargetID ||
		fast[0].Upstream != "https://primary.example/v1" || fast[1].ID != "region_b" ||
		fast[1].Upstream != "https://region-b.example/v1" {
		t.Fatalf("fast targets=%+v", fast)
	}
	strong := runtime.RoutingTargets("strong")
	if len(strong) != 3 || strong[0].ID != PrimaryTargetID ||
		strong[1].ID != "region_b" || strong[2].ID != "strong_backup" {
		t.Fatalf("strong targets=%+v", strong)
	}
	fast[0] = TargetRuntime{}
	if got := runtime.RoutingTargets("fast")[0].ID; got != PrimaryTargetID {
		t.Fatalf("runtime target list followed caller mutation: %q", got)
	}
}

func TestRecordResolveRejectsInvalidTargets(t *testing.T) {
	supportsVision := true
	base := Record{
		Slug: "coding", DisplayName: "Coding", Enabled: true,
		Config: NewConfig(ProtocolAnthropic, "https://primary.example/v1"),
	}
	base.Config.Models = []ModelCapabilityConfig{{ID: "fast", SupportsVision: &supportsVision}}
	tests := []struct {
		name    string
		targets []TargetConfig
	}{
		{name: "reserved ID", targets: []TargetConfig{{ID: PrimaryTargetID, Upstream: "https://b.example", Models: []string{"fast"}}}},
		{name: "invalid ID", targets: []TargetConfig{{ID: "Region B", Upstream: "https://b.example", Models: []string{"fast"}}}},
		{name: "duplicate ID", targets: []TargetConfig{
			{ID: "b", Upstream: "https://b.example", Models: []string{"fast"}},
			{ID: "b", Upstream: "https://c.example", Models: []string{"fast"}},
		}},
		{name: "primary URL", targets: []TargetConfig{{ID: "b", Upstream: "https://primary.example/v1/", Models: []string{"fast"}}}},
		{name: "duplicate URL", targets: []TargetConfig{
			{ID: "b", Upstream: "https://b.example", Models: []string{"fast"}},
			{ID: "c", Upstream: "https://b.example/", Models: []string{"fast"}},
		}},
		{name: "unsafe URL", targets: []TargetConfig{{ID: "b", Upstream: "https://user:secret@b.example", Models: []string{"fast"}}}},
		{name: "no models", targets: []TargetConfig{{ID: "b", Upstream: "https://b.example"}}},
		{name: "unknown model", targets: []TargetConfig{{ID: "b", Upstream: "https://b.example", Models: []string{"other"}}}},
		{name: "duplicate model", targets: []TargetConfig{{ID: "b", Upstream: "https://b.example", Models: []string{"fast", "fast"}}}},
		{name: "model whitespace", targets: []TargetConfig{{ID: "b", Upstream: "https://b.example", Models: []string{" fast"}}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record := base
			record.Config.Targets = tt.targets
			if _, err := record.Resolve(); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("Resolve() error=%v, want invalid config", err)
			}
		})
	}
}
