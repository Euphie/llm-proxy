package strategycompiler

import (
	"context"
	"errors"

	"github.com/Euphie/llm-proxy/internal/evalcatalog"
	"github.com/Euphie/llm-proxy/internal/modelcatalog"
	"github.com/Euphie/llm-proxy/internal/profile"
)

var (
	ErrInvalidIntent            = errors.New("invalid strategy generation intent")
	ErrInsufficientParticipants = errors.New("strategy generation requires at least two participants")
)

const GeneratorVersion = "strategy-compiler/v2"

type Objective string

const (
	ObjectiveBalanced Objective = "balanced"
	ObjectiveQuality  Objective = "quality"
	ObjectiveCost     Objective = "cost"
	ObjectiveLatency  Objective = "latency"
)

type Intent struct {
	Objective                 Objective `json:"objective"`
	Participants              []string  `json:"participants,omitempty"`
	MaxCostPerRequestMicroUSD *int64    `json:"max_cost_per_request_micro_usd,omitempty"`
	LatencyTargetMS           *int      `json:"latency_target_ms,omitempty"`
	DailyEvalBudgetMicroUSD   *int64    `json:"daily_eval_budget_micro_usd,omitempty"`
}

type EvidenceKey struct {
	ProfileID      int64
	Strategy       string
	CandidateModel string
	ReferenceModel string
	Domain         string
	Difficulty     string
}

type LocalEstimate struct {
	QualityMeanBPS      int
	QualityLowerBPS     int
	StabilityLowerBPS   int
	SevereErrorUpperBPS int
	ExpectedLatencyMS   int64
	EffectiveSamples    float64
	Reliable            bool
}

type LocalEvidenceProvider interface {
	CandidateEstimate(context.Context, EvidenceKey) (LocalEstimate, bool, error)
}

type ModelCatalogProvider interface {
	Current() modelcatalog.Catalog
}

type EvaluationCatalogProvider interface {
	Current() evalcatalog.Catalog
}

type Explanation struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Domain  string `json:"domain,omitempty"`
	Route   string `json:"route,omitempty"`
	Model   string `json:"model,omitempty"`
}

type Confidence struct {
	Level                 string `json:"level"`
	LocalCandidates       int    `json:"local_candidates"`
	ExternalCandidates    int    `json:"external_candidates"`
	ProvisionalCandidates int    `json:"provisional_candidates"`
}

type RoleRecommendation struct {
	Apply                   bool     `json:"apply"`
	Participants            []string `json:"participants"`
	StrongBaselineModel     string   `json:"strong_baseline_model"`
	TaskAnalyzerModel       string   `json:"task_analyzer_model"`
	ReviewerModel           string   `json:"reviewer_model"`
	DailyEvalBudgetMicroUSD int64    `json:"daily_eval_budget_micro_usd,omitempty"`
}

type Result struct {
	Config       profile.RoutingStrategyConfig `json:"config"`
	Roles        RoleRecommendation            `json:"roles"`
	Explanations []Explanation                 `json:"explanations"`
	SourceDigest string                        `json:"source_digest"`
	Confidence   Confidence                    `json:"confidence"`
}
