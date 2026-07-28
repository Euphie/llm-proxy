// Package config handles loading and resolving proxy configuration from a YAML file.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Euphie/llm-proxy/internal/provider"

	"gopkg.in/yaml.v3"
)

// Default retry values applied when a rule omits the field.
const (
	defaultListenAddr            = ":8080"
	defaultMaxRetries            = 10
	defaultRetryDelay            = 2 * time.Second
	defaultRetryJitter           = 1 * time.Second
	defaultVisionModel           = "sonnet"
	defaultVisionMaxTokens       = 2048
	defaultVisionTimeout         = 2 * time.Minute
	defaultVisionMaxConcurrency  = 4
	defaultVisionCacheTTL        = 30 * time.Minute
	defaultVisionCacheMaxEntries = 512
	defaultStatsPassword         = "statspwd123456"
)

// Config is the resolved runtime configuration for the active provider.
type Config struct {
	ListenAddr    string
	Upstream      string
	ProviderName  string
	OverloadRules []provider.Rule
	// Protocol identifies the API response format for token usage parsing.
	// Supported values: "anthropic" (default), "openai".
	Protocol string
	// StatsDB is the path to the SQLite database used for token usage statistics.
	// Empty means stats are disabled.
	StatsDB string
	// StatsPassword protects /stats and /stats/data with HTTP Basic Auth.
	StatsPassword string
	Vision        VisionConfig
}

type VisionConfig struct {
	Enabled         bool
	Model           string
	MaxTokens       int
	Timeout         time.Duration
	MaxConcurrency  int
	CacheTTL        time.Duration
	CacheMaxEntries int
	Prompt          string
}

// ---- YAML types ----

type yamlDuration struct{ time.Duration }

func (d *yamlDuration) UnmarshalYAML(value *yaml.Node) error {
	dur, err := time.ParseDuration(value.Value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", value.Value, err)
	}
	d.Duration = dur
	return nil
}

type ruleYAML struct {
	Status       int           `yaml:"status"`
	BodyContains string        `yaml:"body_contains"`
	MaxRetries   *int          `yaml:"max_retries"`
	Delay        *yamlDuration `yaml:"delay"`
	Jitter       *yamlDuration `yaml:"jitter"`
}

type visionYAML struct {
	Enabled         bool          `yaml:"enabled"`
	Model           string        `yaml:"model"`
	MaxTokens       *int          `yaml:"max_tokens"`
	Timeout         *yamlDuration `yaml:"timeout"`
	MaxConcurrency  *int          `yaml:"max_concurrency"`
	CacheTTL        *yamlDuration `yaml:"cache_ttl"`
	CacheMaxEntries *int          `yaml:"cache_max_entries"`
	Prompt          string        `yaml:"prompt"`
}

type providerYAML struct {
	Upstream      string     `yaml:"upstream"`
	Protocol      string     `yaml:"protocol"`
	OverloadRules []ruleYAML `yaml:"overload_rules"`
	Vision        visionYAML `yaml:"vision"`
}

type fileConfig struct {
	Listen        string                  `yaml:"listen"`
	Active        string                  `yaml:"active"`
	StatsDB       string                  `yaml:"stats_db"`
	StatsPassword string                  `yaml:"stats_password"`
	Providers     map[string]providerYAML `yaml:"providers"`
}

// Load reads the YAML config file at path and returns the resolved Config.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}

	var fc fileConfig
	if err := yaml.Unmarshal(data, &fc); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}

	if fc.Active == "" {
		return nil, fmt.Errorf("config: set 'active' in config.yaml")
	}

	pc, ok := fc.Providers[fc.Active]
	if !ok {
		return nil, fmt.Errorf("config: provider %q not found in config.yaml", fc.Active)
	}

	return resolve(fc.Active, pc, fc.Listen, fc.StatsDB, fc.StatsPassword)
}

func resolve(name string, pc providerYAML, listen, statsDB, statsPassword string) (*Config, error) {
	if pc.Upstream == "" {
		return nil, fmt.Errorf("provider %q: upstream URL is required", name)
	}

	if listen == "" {
		listen = defaultListenAddr
	}
	if statsPassword == "" {
		statsPassword = defaultStatsPassword
	}

	rules := make([]provider.Rule, len(pc.OverloadRules))
	for i, r := range pc.OverloadRules {
		rule := provider.Rule{
			Status:       r.Status,
			BodyContains: r.BodyContains,
			MaxRetries:   defaultMaxRetries,
			RetryDelay:   defaultRetryDelay,
			RetryJitter:  defaultRetryJitter,
		}
		if r.MaxRetries != nil {
			rule.MaxRetries = *r.MaxRetries
		}
		if r.Delay != nil {
			rule.RetryDelay = r.Delay.Duration
		}
		if r.Jitter != nil {
			rule.RetryJitter = r.Jitter.Duration
		}
		rules[i] = rule
	}

	protocol := pc.Protocol
	if protocol == "" {
		protocol = "anthropic"
	}
	visionCfg, err := resolveVision(name, protocol, pc.Vision)
	if err != nil {
		return nil, err
	}

	return &Config{
		ListenAddr:    listen,
		Upstream:      strings.TrimRight(pc.Upstream, "/"),
		ProviderName:  name,
		OverloadRules: rules,
		Protocol:      protocol,
		StatsDB:       statsDB,
		StatsPassword: statsPassword,
		Vision:        visionCfg,
	}, nil
}

func resolveVision(name, protocol string, vision visionYAML) (VisionConfig, error) {
	visionCfg := VisionConfig{
		Enabled:         vision.Enabled,
		Model:           defaultVisionModel,
		MaxTokens:       defaultVisionMaxTokens,
		Timeout:         defaultVisionTimeout,
		MaxConcurrency:  defaultVisionMaxConcurrency,
		CacheTTL:        defaultVisionCacheTTL,
		CacheMaxEntries: defaultVisionCacheMaxEntries,
		Prompt:          vision.Prompt,
	}
	if vision.Model != "" {
		visionCfg.Model = vision.Model
	}
	if vision.MaxTokens != nil {
		visionCfg.MaxTokens = *vision.MaxTokens
	}
	if vision.Timeout != nil {
		visionCfg.Timeout = vision.Timeout.Duration
	}
	if vision.MaxConcurrency != nil {
		visionCfg.MaxConcurrency = *vision.MaxConcurrency
	}
	if vision.CacheTTL != nil {
		visionCfg.CacheTTL = vision.CacheTTL.Duration
	}
	if vision.CacheMaxEntries != nil {
		visionCfg.CacheMaxEntries = *vision.CacheMaxEntries
	}

	if !visionCfg.Enabled {
		return visionCfg, nil
	}
	if protocol != "anthropic" {
		return VisionConfig{}, fmt.Errorf("provider %q: vision requires protocol anthropic", name)
	}
	if visionCfg.MaxTokens <= 0 {
		return VisionConfig{}, fmt.Errorf("provider %q: vision.max_tokens must be positive", name)
	}
	if visionCfg.Timeout <= 0 {
		return VisionConfig{}, fmt.Errorf("provider %q: vision.timeout must be positive", name)
	}
	if visionCfg.MaxConcurrency <= 0 {
		return VisionConfig{}, fmt.Errorf("provider %q: vision.max_concurrency must be positive", name)
	}
	if visionCfg.CacheTTL <= 0 {
		return VisionConfig{}, fmt.Errorf("provider %q: vision.cache_ttl must be positive", name)
	}
	if visionCfg.CacheMaxEntries <= 0 {
		return VisionConfig{}, fmt.Errorf("provider %q: vision.cache_max_entries must be positive", name)
	}
	return visionCfg, nil
}
