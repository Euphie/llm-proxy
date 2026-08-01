package profile

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

var (
	strategyNamePattern = regexp.MustCompile(`^\d{8}-\d{3}$`)
	routingIDPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
)

const defaultRoutingSessionTTL = 24 * time.Hour

const maxDynamicOptimizationTaskTimeout = 10 * time.Minute

type AutoRoutingConfig struct {
	Enabled                  bool                      `json:"enabled"`
	Participants             []string                  `json:"participants,omitempty"`
	StrongBaselineModel      string                    `json:"strong_baseline_model,omitempty"`
	TaskAnalyzerModel        string                    `json:"task_analyzer_model,omitempty"`
	AnalyzerTimeout          string                    `json:"analyzer_timeout,omitempty"`
	AnalyzerMinConfidenceBPS int                       `json:"analyzer_min_confidence_bps,omitempty"`
	SessionTTL               string                    `json:"session_ttl,omitempty"`
	DynamicOptimization      DynamicOptimizationConfig `json:"dynamic_optimization,omitempty"`
	Strategy                 RoutingStrategyConfig     `json:"strategy,omitempty"`
}

type DynamicOptimizationConfig struct {
	Enabled             bool   `json:"enabled"`
	SampleRateBPS       int    `json:"sample_rate_bps,omitempty"`
	DailyBudgetMicroUSD int64  `json:"daily_budget_micro_usd,omitempty"`
	ReviewerModel       string `json:"reviewer_model,omitempty"`
	MaxConcurrency      int    `json:"max_concurrency,omitempty"`
	QueueCapacity       int    `json:"queue_capacity,omitempty"`
	TaskTimeout         string `json:"task_timeout,omitempty"`
}

type RoutingStrategyConfig struct {
	Name         string              `json:"name"`
	Alias        string              `json:"alias,omitempty"`
	DefaultRoute string              `json:"default_route"`
	TaskRoutes   []TaskRouteConfig   `json:"task_routes,omitempty"`
	Routes       []RouteConfig       `json:"routes"`
	Budget       AttemptBudgetConfig `json:"budget"`
}

type TaskRouteConfig struct {
	TaskType string `json:"task_type"`
	Route    string `json:"route"`
}

type RouteConfig struct {
	ID                    string                 `json:"id"`
	MinQualityBPS         int                    `json:"min_quality_bps"`
	MaxSevereErrorRateBPS int                    `json:"max_severe_error_rate_bps"`
	Candidates            []RouteCandidateConfig `json:"candidates"`
}

type RouteCandidateConfig struct {
	Model              string `json:"model"`
	QualityScoreBPS    int    `json:"quality_score_bps"`
	SevereErrorRateBPS int    `json:"severe_error_rate_bps"`
}

type AttemptBudgetConfig struct {
	MaxAnswerAttempts        int    `json:"max_answer_attempts"`
	MaxAuxiliaryCalls        int    `json:"max_auxiliary_calls"`
	MaxTotalOutboundCalls    int    `json:"max_total_outbound_calls"`
	MaxRetriesPerTarget      int    `json:"max_retries_per_target"`
	MaxTargetSwitches        int    `json:"max_target_switches"`
	MaxModelSwitches         int    `json:"max_model_switches"`
	Deadline                 string `json:"deadline"`
	MaxWorstCaseCostMicroUSD int64  `json:"max_worst_case_cost_micro_usd"`
}

type AutoRoutingRuntime struct {
	Enabled                  bool
	Participants             []string
	StrongBaselineModel      string
	TaskAnalyzerModel        string
	AnalyzerTimeout          time.Duration
	AnalyzerMinConfidenceBPS int
	SessionTTL               time.Duration
	DynamicOptimization      DynamicOptimizationRuntime
	Strategy                 RoutingStrategyRuntime

	participantSet map[string]struct{}
}

type DynamicOptimizationRuntime struct {
	Enabled             bool
	SampleRateBPS       int
	DailyBudgetMicroUSD int64
	ReviewerModel       string
	MaxConcurrency      int
	QueueCapacity       int
	TaskTimeout         time.Duration
}

type RoutingStrategyRuntime struct {
	Name         string
	Alias        string
	DefaultRoute string
	TaskRoutes   map[string]string
	Routes       map[string]RouteRuntime
	Budget       AttemptBudgetRuntime
}

type RouteRuntime struct {
	ID                    string
	MinQualityBPS         int
	MaxSevereErrorRateBPS int
	Candidates            []RouteCandidateRuntime
}

type RouteCandidateRuntime struct {
	Model              string
	QualityScoreBPS    int
	SevereErrorRateBPS int
}

type AttemptBudgetRuntime struct {
	MaxAnswerAttempts        int
	MaxAuxiliaryCalls        int
	MaxTotalOutboundCalls    int
	MaxRetriesPerTarget      int
	MaxTargetSwitches        int
	MaxModelSwitches         int
	Deadline                 time.Duration
	MaxWorstCaseCostMicroUSD int64
}

func (r AutoRoutingRuntime) Participates(model string) bool {
	_, ok := r.participantSet[model]
	return ok
}

func resolveAutoRouting(
	config AutoRoutingConfig,
	models ModelCatalog,
	vision VisionRuntime,
) (AutoRoutingRuntime, error) {
	if !config.Enabled {
		return AutoRoutingRuntime{}, nil
	}

	participants, participantSet, err := resolveParticipants(config.Participants, models)
	if err != nil {
		return AutoRoutingRuntime{}, err
	}
	baseline := strings.TrimSpace(config.StrongBaselineModel)
	if baseline == "" || baseline != config.StrongBaselineModel {
		return AutoRoutingRuntime{}, invalidAuto("strong baseline model is required without surrounding whitespace")
	}
	if _, ok := participantSet[baseline]; !ok {
		return AutoRoutingRuntime{}, invalidAuto("strong baseline model %q must be a participant", baseline)
	}
	analyzer := strings.TrimSpace(config.TaskAnalyzerModel)
	if analyzer == "" || analyzer != config.TaskAnalyzerModel {
		return AutoRoutingRuntime{}, invalidAuto("task analyzer model is required without surrounding whitespace")
	}
	if _, ok := models[analyzer]; !ok {
		return AutoRoutingRuntime{}, invalidAuto("task analyzer model %q is not in the model catalog", analyzer)
	}

	requiredModels := make(map[string]struct{}, len(participantSet)+2)
	for model := range participantSet {
		requiredModels[model] = struct{}{}
	}
	requiredModels[analyzer] = struct{}{}
	if vision.Enabled {
		if _, ok := models[vision.Model]; !ok {
			return AutoRoutingRuntime{}, invalidAuto("vision model %q is not in the model catalog", vision.Model)
		}
		requiredModels[vision.Model] = struct{}{}
	}
	dynamicOptimization, err := resolveDynamicOptimization(config.DynamicOptimization, models)
	if err != nil {
		return AutoRoutingRuntime{}, err
	}
	if dynamicOptimization.Enabled {
		requiredModels[dynamicOptimization.ReviewerModel] = struct{}{}
	}
	for model := range requiredModels {
		capability := models[model]
		if !capability.HasInputPrice || !capability.HasOutputPrice {
			return AutoRoutingRuntime{}, invalidAuto("model %q requires input and output prices", model)
		}
	}

	analyzerTimeout, err := time.ParseDuration(config.AnalyzerTimeout)
	if err != nil || analyzerTimeout <= 0 {
		return AutoRoutingRuntime{}, invalidAuto("analyzer timeout must be a positive duration")
	}
	if config.AnalyzerMinConfidenceBPS < 0 || config.AnalyzerMinConfidenceBPS > 10_000 {
		return AutoRoutingRuntime{}, invalidAuto("analyzer minimum confidence must be between 0 and 10000")
	}
	sessionTTL := defaultRoutingSessionTTL
	if config.SessionTTL != "" {
		sessionTTL, err = time.ParseDuration(config.SessionTTL)
		if err != nil || sessionTTL < 5*time.Minute || sessionTTL > 30*24*time.Hour {
			return AutoRoutingRuntime{}, invalidAuto("Session TTL must be between 5m and 720h")
		}
	}

	strategy, err := resolveRoutingStrategy(config.Strategy, participantSet)
	if err != nil {
		return AutoRoutingRuntime{}, err
	}
	if analyzerTimeout > strategy.Budget.Deadline {
		return AutoRoutingRuntime{}, invalidAuto("analyzer timeout must not exceed the strategy deadline")
	}

	return AutoRoutingRuntime{
		Enabled:                  true,
		Participants:             participants,
		StrongBaselineModel:      baseline,
		TaskAnalyzerModel:        analyzer,
		AnalyzerTimeout:          analyzerTimeout,
		AnalyzerMinConfidenceBPS: config.AnalyzerMinConfidenceBPS,
		SessionTTL:               sessionTTL,
		DynamicOptimization:      dynamicOptimization,
		Strategy:                 strategy,
		participantSet:           participantSet,
	}, nil
}

func resolveDynamicOptimization(
	config DynamicOptimizationConfig,
	models ModelCatalog,
) (DynamicOptimizationRuntime, error) {
	if !config.Enabled {
		return DynamicOptimizationRuntime{}, nil
	}
	reviewer := strings.TrimSpace(config.ReviewerModel)
	if reviewer == "" || reviewer != config.ReviewerModel {
		return DynamicOptimizationRuntime{}, invalidAuto(
			"quality reviewer model is required without surrounding whitespace",
		)
	}
	model, ok := models[reviewer]
	if !ok {
		return DynamicOptimizationRuntime{}, invalidAuto(
			"quality reviewer model %q is not in the model catalog", reviewer,
		)
	}
	if !model.HasInputPrice || !model.HasOutputPrice {
		return DynamicOptimizationRuntime{}, invalidAuto(
			"quality reviewer model %q requires input and output prices", reviewer,
		)
	}
	if !model.HasContextWindow || !model.HasMaxOutputTokens || model.MaxOutputTokens < 256 {
		return DynamicOptimizationRuntime{}, invalidAuto(
			"quality reviewer model %q requires a context window and at least 256 output tokens", reviewer,
		)
	}
	if config.SampleRateBPS <= 0 || config.SampleRateBPS > 10_000 {
		return DynamicOptimizationRuntime{}, invalidAuto(
			"dynamic optimization sample rate must be between 1 and 10000",
		)
	}
	if config.DailyBudgetMicroUSD <= 0 || config.DailyBudgetMicroUSD > maxBrowserSafeInteger {
		return DynamicOptimizationRuntime{}, invalidAuto(
			"dynamic optimization daily budget must be a positive browser-safe integer",
		)
	}
	if config.MaxConcurrency <= 0 || config.MaxConcurrency > 32 {
		return DynamicOptimizationRuntime{}, invalidAuto(
			"dynamic optimization concurrency must be between 1 and 32",
		)
	}
	if config.QueueCapacity <= 0 || config.QueueCapacity > 4096 {
		return DynamicOptimizationRuntime{}, invalidAuto(
			"dynamic optimization queue capacity must be between 1 and 4096",
		)
	}
	taskTimeout, err := time.ParseDuration(config.TaskTimeout)
	if err != nil || taskTimeout <= 0 || taskTimeout > maxDynamicOptimizationTaskTimeout {
		return DynamicOptimizationRuntime{}, invalidAuto(
			"dynamic optimization task timeout must be between 1ns and 10m",
		)
	}
	return DynamicOptimizationRuntime{
		Enabled:             true,
		SampleRateBPS:       config.SampleRateBPS,
		DailyBudgetMicroUSD: config.DailyBudgetMicroUSD,
		ReviewerModel:       reviewer,
		MaxConcurrency:      config.MaxConcurrency,
		QueueCapacity:       config.QueueCapacity,
		TaskTimeout:         taskTimeout,
	}, nil
}

func resolveParticipants(
	configured []string,
	models ModelCatalog,
) ([]string, map[string]struct{}, error) {
	if len(configured) == 0 {
		return nil, nil, invalidAuto("at least one participant is required")
	}
	participants := make([]string, 0, len(configured))
	participantSet := make(map[string]struct{}, len(configured))
	for _, model := range configured {
		if model == "" || strings.TrimSpace(model) != model {
			return nil, nil, invalidAuto("participant model IDs cannot be empty or contain surrounding whitespace")
		}
		if _, ok := models[model]; !ok {
			return nil, nil, invalidAuto("participant model %q is not in the model catalog", model)
		}
		if _, duplicate := participantSet[model]; duplicate {
			return nil, nil, invalidAuto("participant model %q is duplicated", model)
		}
		participantSet[model] = struct{}{}
		participants = append(participants, model)
	}
	return participants, participantSet, nil
}

func resolveRoutingStrategy(
	config RoutingStrategyConfig,
	participants map[string]struct{},
) (RoutingStrategyRuntime, error) {
	if !strategyNamePattern.MatchString(config.Name) {
		return RoutingStrategyRuntime{}, invalidAuto("strategy name must match YYYYMMDD-NNN")
	}
	if strings.TrimSpace(config.Alias) != config.Alias {
		return RoutingStrategyRuntime{}, invalidAuto("strategy alias cannot contain surrounding whitespace")
	}
	if !routingIDPattern.MatchString(config.DefaultRoute) {
		return RoutingStrategyRuntime{}, invalidAuto("default route is invalid")
	}
	if len(config.Routes) == 0 {
		return RoutingStrategyRuntime{}, invalidAuto("strategy requires at least one route")
	}

	routes := make(map[string]RouteRuntime, len(config.Routes))
	for _, configured := range config.Routes {
		route, err := resolveRoute(configured, participants)
		if err != nil {
			return RoutingStrategyRuntime{}, err
		}
		if _, duplicate := routes[route.ID]; duplicate {
			return RoutingStrategyRuntime{}, invalidAuto("route %q is duplicated", route.ID)
		}
		routes[route.ID] = route
	}
	if _, ok := routes[config.DefaultRoute]; !ok {
		return RoutingStrategyRuntime{}, invalidAuto("default route %q does not exist", config.DefaultRoute)
	}

	taskRoutes := make(map[string]string, len(config.TaskRoutes))
	for _, configured := range config.TaskRoutes {
		if !routingIDPattern.MatchString(configured.TaskType) {
			return RoutingStrategyRuntime{}, invalidAuto("task type %q is invalid", configured.TaskType)
		}
		if _, duplicate := taskRoutes[configured.TaskType]; duplicate {
			return RoutingStrategyRuntime{}, invalidAuto("task type %q is duplicated", configured.TaskType)
		}
		if _, ok := routes[configured.Route]; !ok {
			return RoutingStrategyRuntime{}, invalidAuto("task type %q references unknown route %q", configured.TaskType, configured.Route)
		}
		taskRoutes[configured.TaskType] = configured.Route
	}

	budget, err := resolveAttemptBudget(config.Budget)
	if err != nil {
		return RoutingStrategyRuntime{}, err
	}
	return RoutingStrategyRuntime{
		Name:         config.Name,
		Alias:        config.Alias,
		DefaultRoute: config.DefaultRoute,
		TaskRoutes:   taskRoutes,
		Routes:       routes,
		Budget:       budget,
	}, nil
}

func resolveRoute(
	config RouteConfig,
	participants map[string]struct{},
) (RouteRuntime, error) {
	if !routingIDPattern.MatchString(config.ID) {
		return RouteRuntime{}, invalidAuto("route ID %q is invalid", config.ID)
	}
	if config.MinQualityBPS < 0 || config.MinQualityBPS > 10_000 {
		return RouteRuntime{}, invalidAuto("route %q minimum quality must be between 0 and 10000", config.ID)
	}
	if config.MaxSevereErrorRateBPS < 0 || config.MaxSevereErrorRateBPS > 10_000 {
		return RouteRuntime{}, invalidAuto("route %q maximum severe error rate must be between 0 and 10000", config.ID)
	}
	if len(config.Candidates) == 0 {
		return RouteRuntime{}, invalidAuto("route %q requires at least one candidate", config.ID)
	}

	candidates := make([]RouteCandidateRuntime, 0, len(config.Candidates))
	seen := make(map[string]struct{}, len(config.Candidates))
	for _, candidate := range config.Candidates {
		if _, ok := participants[candidate.Model]; !ok {
			return RouteRuntime{}, invalidAuto("route %q candidate %q is not a participant", config.ID, candidate.Model)
		}
		if _, duplicate := seen[candidate.Model]; duplicate {
			return RouteRuntime{}, invalidAuto("route %q candidate %q is duplicated", config.ID, candidate.Model)
		}
		if candidate.QualityScoreBPS < 0 || candidate.QualityScoreBPS > 10_000 ||
			candidate.SevereErrorRateBPS < 0 || candidate.SevereErrorRateBPS > 10_000 {
			return RouteRuntime{}, invalidAuto("route %q candidate %q quality metrics must be between 0 and 10000", config.ID, candidate.Model)
		}
		seen[candidate.Model] = struct{}{}
		candidates = append(candidates, RouteCandidateRuntime(candidate))
	}
	return RouteRuntime{
		ID:                    config.ID,
		MinQualityBPS:         config.MinQualityBPS,
		MaxSevereErrorRateBPS: config.MaxSevereErrorRateBPS,
		Candidates:            candidates,
	}, nil
}

func resolveAttemptBudget(config AttemptBudgetConfig) (AttemptBudgetRuntime, error) {
	deadline, err := time.ParseDuration(config.Deadline)
	if err != nil || deadline <= 0 {
		return AttemptBudgetRuntime{}, invalidAuto("strategy deadline must be a positive duration")
	}
	if config.MaxAnswerAttempts <= 0 || config.MaxAuxiliaryCalls <= 0 ||
		config.MaxTotalOutboundCalls < 2 {
		return AttemptBudgetRuntime{}, invalidAuto("strategy call limits must allow an analyzer and an answer")
	}
	if config.MaxRetriesPerTarget < 0 || config.MaxTargetSwitches < 0 ||
		config.MaxModelSwitches < 0 || config.MaxWorstCaseCostMicroUSD < 0 {
		return AttemptBudgetRuntime{}, invalidAuto("strategy retry, switch, and cost limits cannot be negative")
	}
	if config.MaxWorstCaseCostMicroUSD > maxBrowserSafeInteger {
		return AttemptBudgetRuntime{}, invalidAuto("strategy maximum worst-case cost exceeds the browser safe integer limit")
	}
	return AttemptBudgetRuntime{
		MaxAnswerAttempts:        config.MaxAnswerAttempts,
		MaxAuxiliaryCalls:        config.MaxAuxiliaryCalls,
		MaxTotalOutboundCalls:    config.MaxTotalOutboundCalls,
		MaxRetriesPerTarget:      config.MaxRetriesPerTarget,
		MaxTargetSwitches:        config.MaxTargetSwitches,
		MaxModelSwitches:         config.MaxModelSwitches,
		Deadline:                 deadline,
		MaxWorstCaseCostMicroUSD: config.MaxWorstCaseCostMicroUSD,
	}, nil
}

func invalidAuto(format string, args ...any) error {
	return fmt.Errorf("%w: auto routing: %s", ErrInvalidConfig, fmt.Sprintf(format, args...))
}
