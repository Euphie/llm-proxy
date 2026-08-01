// Package profile defines persisted Profile configuration and its runtime form.
package profile

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Euphie/llm-proxy/internal/provider"
)

var (
	ErrInvalidSlug     = errors.New("invalid profile slug")
	ErrInvalidProtocol = errors.New("invalid profile protocol")
	ErrInvalidConfig   = errors.New("invalid profile config")
)

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

type Protocol string

const (
	ProtocolAnthropic Protocol = "anthropic"
	ProtocolOpenAI    Protocol = "openai"
)

type VisionTransport string

const (
	VisionTransportAnthropicMessages     VisionTransport = "anthropic_messages"
	VisionTransportOpenAIResponses       VisionTransport = "openai_responses"
	VisionTransportOpenAIChatCompletions VisionTransport = "openai_chat_completions"
)

type VisionConfig struct {
	Enabled             bool                `json:"enabled"`
	Transport           VisionTransport     `json:"transport,omitempty"`
	Model               string              `json:"model"`
	UnlistedModelPolicy UnlistedModelPolicy `json:"unlisted_model_policy"`
	MaxTokens           int                 `json:"max_tokens"`
	Timeout             string              `json:"timeout"`
	MaxConcurrency      int                 `json:"max_concurrency"`
	CacheTTL            string              `json:"cache_ttl"`
	CacheMaxEntries     int                 `json:"cache_max_entries"`
	Prompt              string              `json:"prompt,omitempty"`
}

type RetryRule struct {
	Status       int    `json:"status"`
	BodyContains string `json:"body_contains,omitempty"`
	MaxRetries   int    `json:"max_retries"`
	Delay        string `json:"delay"`
	Jitter       string `json:"jitter"`
}

type Config struct {
	Version         int                     `json:"version"`
	Protocol        Protocol                `json:"protocol"`
	Upstream        string                  `json:"upstream"`
	ProviderID      string                  `json:"provider_id,omitempty"`
	CredentialScope string                  `json:"credential_scope,omitempty"`
	Targets         []TargetConfig          `json:"targets,omitempty"`
	Models          []ModelCapabilityConfig `json:"models,omitempty"`
	AutoRouting     AutoRoutingConfig       `json:"auto_routing,omitempty"`
	Vision          VisionConfig            `json:"vision"`
	OverloadRules   []RetryRule             `json:"overload_rules"`
}

type Record struct {
	ID          int64
	Slug        string
	DisplayName string
	Enabled     bool
	Config      Config
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type VisionRuntime struct {
	Enabled             bool
	Transport           VisionTransport
	Model               string
	UnlistedModelPolicy UnlistedModelPolicy
	MaxTokens           int
	Timeout             time.Duration
	MaxConcurrency      int
	CacheTTL            time.Duration
	CacheMaxEntries     int
	Prompt              string
}

type Runtime struct {
	ID            int64
	Slug          string
	DisplayName   string
	Enabled       bool
	Protocol      Protocol
	Upstream      string
	Targets       []TargetRuntime
	Models        ModelCatalog
	AutoRouting   AutoRoutingRuntime
	Vision        VisionRuntime
	OverloadRules []provider.Rule
}

func NewConfig(protocol Protocol, upstream string) Config {
	return Config{
		Version:  1,
		Protocol: protocol,
		Upstream: upstream,
		Vision: VisionConfig{
			Enabled:         false,
			Transport:       DefaultVisionTransport(protocol),
			Model:           "sonnet",
			MaxTokens:       2048,
			Timeout:         "2m",
			MaxConcurrency:  4,
			CacheTTL:        "30m",
			CacheMaxEntries: 512,
		},
	}
}

func DefaultVisionTransport(protocol Protocol) VisionTransport {
	if protocol == ProtocolOpenAI {
		return VisionTransportOpenAIChatCompletions
	}
	return VisionTransportAnthropicMessages
}

func (r Record) Resolve() (Runtime, error) {
	if !slugPattern.MatchString(r.Slug) || r.Slug == "v1" {
		return Runtime{}, fmt.Errorf("%w: %q", ErrInvalidSlug, r.Slug)
	}
	if strings.TrimSpace(r.DisplayName) == "" {
		return Runtime{}, fmt.Errorf("%w: display name is required", ErrInvalidConfig)
	}
	if r.Config.Protocol != ProtocolAnthropic && r.Config.Protocol != ProtocolOpenAI {
		return Runtime{}, fmt.Errorf("%w: %q", ErrInvalidProtocol, r.Config.Protocol)
	}

	upstream, err := resolveUpstream(r.Config.Upstream)
	if err != nil {
		return Runtime{}, err
	}
	vision, err := resolveVision(r.Config.Protocol, r.Config.Vision)
	if err != nil {
		return Runtime{}, err
	}
	models, err := resolveModelCatalog(r.Config.Models)
	if err != nil {
		return Runtime{}, err
	}
	targets, err := resolveTargets(
		upstream,
		r.Config.ProviderID,
		r.Config.CredentialScope,
		r.Config.Targets,
		models,
	)
	if err != nil {
		return Runtime{}, err
	}
	autoRouting, err := resolveAutoRouting(r.Config.AutoRouting, models, vision)
	if err != nil {
		return Runtime{}, err
	}
	rules, err := resolveRetryRules(r.Config.OverloadRules)
	if err != nil {
		return Runtime{}, err
	}

	return Runtime{
		ID:            r.ID,
		Slug:          r.Slug,
		DisplayName:   r.DisplayName,
		Enabled:       r.Enabled,
		Protocol:      r.Config.Protocol,
		Upstream:      upstream,
		Targets:       targets,
		Models:        models,
		AutoRouting:   autoRouting,
		Vision:        vision,
		OverloadRules: rules,
	}, nil
}

func resolveUpstream(raw string) (string, error) {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil {
		return "", fmt.Errorf("%w: upstream URL: %v", ErrInvalidConfig, err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", fmt.Errorf("%w: unsafe upstream URL", ErrInvalidConfig)
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func resolveVision(protocol Protocol, config VisionConfig) (VisionRuntime, error) {
	model := strings.TrimSpace(config.Model)
	if config.Enabled && model == "" {
		return VisionRuntime{}, fmt.Errorf(
			"%w: vision model is required when vision is enabled",
			ErrInvalidConfig,
		)
	}
	policy := config.UnlistedModelPolicy
	if policy == "" {
		policy = UnlistedModelBypass
	}
	if policy != UnlistedModelBypass && policy != UnlistedModelEnhance {
		return VisionRuntime{}, fmt.Errorf("%w: unsupported unlisted model policy %q", ErrInvalidConfig, policy)
	}
	transport := config.Transport
	if transport == "" {
		transport = DefaultVisionTransport(protocol)
	}
	switch protocol {
	case ProtocolAnthropic:
		if transport != VisionTransportAnthropicMessages {
			return VisionRuntime{}, fmt.Errorf(
				"%w: vision transport %q is not supported by anthropic",
				ErrInvalidConfig,
				transport,
			)
		}
	case ProtocolOpenAI:
		if transport != VisionTransportOpenAIResponses &&
			transport != VisionTransportOpenAIChatCompletions {
			return VisionRuntime{}, fmt.Errorf(
				"%w: vision transport %q is not supported by openai",
				ErrInvalidConfig,
				transport,
			)
		}
	}
	timeout, err := time.ParseDuration(config.Timeout)
	if err != nil {
		return VisionRuntime{}, fmt.Errorf("%w: vision timeout: %v", ErrInvalidConfig, err)
	}
	cacheTTL, err := time.ParseDuration(config.CacheTTL)
	if err != nil {
		return VisionRuntime{}, fmt.Errorf("%w: vision cache TTL: %v", ErrInvalidConfig, err)
	}
	if config.Enabled && (config.MaxTokens <= 0 || timeout <= 0 || config.MaxConcurrency <= 0 ||
		cacheTTL <= 0 || config.CacheMaxEntries <= 0) {
		return VisionRuntime{}, fmt.Errorf("%w: vision limits must be positive", ErrInvalidConfig)
	}

	return VisionRuntime{
		Enabled:             config.Enabled,
		Transport:           transport,
		Model:               model,
		UnlistedModelPolicy: policy,
		MaxTokens:           config.MaxTokens,
		Timeout:             timeout,
		MaxConcurrency:      config.MaxConcurrency,
		CacheTTL:            cacheTTL,
		CacheMaxEntries:     config.CacheMaxEntries,
		Prompt:              config.Prompt,
	}, nil
}

func resolveRetryRules(configs []RetryRule) ([]provider.Rule, error) {
	rules := make([]provider.Rule, len(configs))
	for i, config := range configs {
		if !provider.IsRetryableStatus(config.Status) {
			return nil, fmt.Errorf(
				"%w: retry rule %d status must be 408, 425, 429, or 500..599",
				ErrInvalidConfig,
				i,
			)
		}
		delay, err := time.ParseDuration(config.Delay)
		if err != nil {
			return nil, fmt.Errorf("%w: retry rule %d delay: %v", ErrInvalidConfig, i, err)
		}
		jitter, err := time.ParseDuration(config.Jitter)
		if err != nil {
			return nil, fmt.Errorf("%w: retry rule %d jitter: %v", ErrInvalidConfig, i, err)
		}
		rules[i] = provider.Rule{
			Status:       config.Status,
			BodyContains: config.BodyContains,
			MaxRetries:   config.MaxRetries,
			RetryDelay:   delay,
			RetryJitter:  jitter,
		}
	}
	return rules, nil
}
