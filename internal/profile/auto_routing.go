package profile

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	strategyNamePattern = regexp.MustCompile(`^\d{8}-\d{3}$`)
	routingIDPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)
)

const defaultRoutingSessionTTL = 24 * time.Hour

const (
	VisionImagePromptReserveTokens        = 4096
	VisionDescriptionBytesPerToken        = 4
	VisionDescriptionWrapperReserveTokens = 64
)

const maxDynamicOptimizationTaskTimeout = 10 * time.Minute

const (
	defaultLongContextThresholdBPS = 7500
	maxRiskPatterns                = 64
	maxRiskPatternRunes            = 128
	RiskPolicyVersion2             = 2
)

var defaultSensitiveTextPatterns = []string{
	"delete production", "drop table", "deploy to production", "rotate credential",
	"删除生产", "清空数据库", "部署到生产", "修改密钥", "转账", "付款",
}

var defaultSensitiveToolPatterns = []string{
	"delete_production", "deploy_production", "rotate_credential", "rotate_secret",
	"payment", "transfer",
	"database_mutation", "database_write", "database_delete", "db_write", "db_delete",
}

type AutoRoutingConfig struct {
	Enabled                  bool                      `json:"enabled"`
	Participants             []string                  `json:"participants,omitempty"`
	StrongBaselineModel      string                    `json:"strong_baseline_model,omitempty"`
	TaskAnalyzerModel        string                    `json:"task_analyzer_model,omitempty"`
	AnalyzerTimeout          string                    `json:"analyzer_timeout,omitempty"`
	AnalyzerMinConfidenceBPS int                       `json:"analyzer_min_confidence_bps,omitempty"`
	SessionTTL               string                    `json:"session_ttl,omitempty"`
	RiskPolicy               RiskPolicyConfig          `json:"risk_policy,omitempty"`
	SelfEscalation           SelfEscalationConfig      `json:"self_escalation,omitempty"`
	DynamicOptimization      DynamicOptimizationConfig `json:"dynamic_optimization,omitempty"`
	Strategy                 RoutingStrategyConfig     `json:"strategy,omitempty"`
}

type RiskPolicyConfig struct {
	Version                  int      `json:"version,omitempty"`
	SensitiveTextPatterns    []string `json:"sensitive_text_patterns"`
	SensitiveToolPatterns    []string `json:"sensitive_tool_patterns"`
	StructuredOutputHighRisk bool     `json:"structured_output_high_risk"`
	LongContextThresholdBPS  int      `json:"long_context_threshold_bps"`
}

type SelfEscalationConfig struct {
	Enabled bool `json:"enabled"`
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
	TaskType   string `json:"task_type,omitempty"`
	Difficulty string `json:"difficulty,omitempty"`
	Route      string `json:"route"`
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
	RiskPolicy               RiskPolicyRuntime
	SelfEscalation           SelfEscalationRuntime
	DynamicOptimization      DynamicOptimizationRuntime
	Strategy                 RoutingStrategyRuntime

	participantSet map[string]struct{}
}

type RiskPolicyRuntime struct {
	Version                  int
	SensitiveTextPatterns    []string
	SensitiveToolPatterns    []string
	StructuredOutputHighRisk bool
	LongContextThresholdBPS  int
}

type SelfEscalationRuntime struct {
	Enabled bool
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
	TaskRoutes   map[TaskRouteKey]string
	Routes       map[string]RouteRuntime
	Budget       AttemptBudgetRuntime
}

type TaskRouteKey struct {
	TaskType   string
	Difficulty string
}

func (r RoutingStrategyRuntime) RouteFor(taskType, difficulty string) (string, bool) {
	if taskType != "" && difficulty != "" && difficulty != "unknown" {
		if route, ok := r.TaskRoutes[TaskRouteKey{TaskType: taskType, Difficulty: difficulty}]; ok {
			return route, true
		}
	}
	if taskType != "" {
		if route, ok := r.TaskRoutes[TaskRouteKey{TaskType: taskType}]; ok {
			return route, true
		}
	}
	if difficulty != "" && difficulty != "unknown" {
		if route, ok := r.TaskRoutes[TaskRouteKey{Difficulty: difficulty}]; ok {
			return route, true
		}
	}
	return "", false
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
	if len(models) < 2 {
		return AutoRoutingRuntime{}, invalidAuto("at least two recorded models are required")
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
	if capability := models[analyzer]; !capability.HasSupportsTools || !capability.SupportsTools {
		return AutoRoutingRuntime{}, invalidAuto("task analyzer model %q must confirm tool support", analyzer)
	}

	requiredModels := make(map[string]struct{}, len(participantSet)+2)
	for model := range participantSet {
		requiredModels[model] = struct{}{}
	}
	requiredModels[analyzer] = struct{}{}
	if vision.Enabled {
		visionModel, ok := models[vision.Model]
		if !ok {
			return AutoRoutingRuntime{}, invalidAuto("vision model %q is not in the model catalog", vision.Model)
		}
		if !visionModel.SupportsVision {
			return AutoRoutingRuntime{}, invalidAuto("vision model %q must confirm vision support", vision.Model)
		}
		if !visionModel.HasContextWindow || !visionModel.HasMaxOutputTokens {
			return AutoRoutingRuntime{}, invalidAuto("vision model %q requires a context window and max output", vision.Model)
		}
		if vision.MaxTokens > visionModel.MaxOutputTokens {
			return AutoRoutingRuntime{}, invalidAuto("vision model %q output reserve exceeds its max output", vision.Model)
		}
		if VisionImagePromptReserveTokens > visionModel.ContextWindow-vision.MaxTokens {
			return AutoRoutingRuntime{}, invalidAuto("vision model %q cannot fit one image prompt reserve", vision.Model)
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
	riskPolicy, err := resolveRiskPolicy(config.RiskPolicy)
	if err != nil {
		return AutoRoutingRuntime{}, err
	}

	strategy, err := resolveRoutingStrategy(config.Strategy, participantSet)
	if err != nil {
		return AutoRoutingRuntime{}, err
	}
	if analyzerTimeout > strategy.Budget.Deadline {
		return AutoRoutingRuntime{}, invalidAuto("analyzer timeout must not exceed the strategy deadline")
	}
	if config.SelfEscalation.Enabled && strategy.Budget.MaxModelSwitches < 1 {
		return AutoRoutingRuntime{}, invalidAuto("self escalation requires at least one model switch")
	}
	if config.SelfEscalation.Enabled {
		checked := make(map[string]struct{})
		for _, route := range strategy.Routes {
			for _, candidate := range route.Candidates {
				if candidate.Model == baseline {
					continue
				}
				if _, ok := checked[candidate.Model]; ok {
					continue
				}
				checked[candidate.Model] = struct{}{}
				capability := models[candidate.Model]
				if !capability.HasSupportsTools || !capability.SupportsTools {
					return AutoRoutingRuntime{}, invalidAuto(
						"self escalation candidate model %q must confirm tool support",
						candidate.Model,
					)
				}
			}
		}
	}

	return AutoRoutingRuntime{
		Enabled:                  true,
		Participants:             participants,
		StrongBaselineModel:      baseline,
		TaskAnalyzerModel:        analyzer,
		AnalyzerTimeout:          analyzerTimeout,
		AnalyzerMinConfidenceBPS: config.AnalyzerMinConfidenceBPS,
		SessionTTL:               sessionTTL,
		RiskPolicy:               riskPolicy,
		SelfEscalation:           SelfEscalationRuntime{Enabled: config.SelfEscalation.Enabled},
		DynamicOptimization:      dynamicOptimization,
		Strategy:                 strategy,
		participantSet:           participantSet,
	}, nil
}

func resolveRiskPolicy(config RiskPolicyConfig) (RiskPolicyRuntime, error) {
	if config.Version != 0 && config.Version != RiskPolicyVersion2 {
		return RiskPolicyRuntime{}, invalidAuto("risk policy version must be %d", RiskPolicyVersion2)
	}
	if config.Version == 0 {
		config = RiskPolicyConfig{Version: RiskPolicyVersion2}
	}
	textPatterns, err := resolveRiskPatterns(
		config.SensitiveTextPatterns,
		defaultSensitiveTextPatterns,
		"sensitive text",
	)
	if err != nil {
		return RiskPolicyRuntime{}, err
	}
	toolPatterns, err := resolveRiskPatterns(
		config.SensitiveToolPatterns,
		defaultSensitiveToolPatterns,
		"sensitive tool",
	)
	if err != nil {
		return RiskPolicyRuntime{}, err
	}
	threshold := config.LongContextThresholdBPS
	if threshold == 0 {
		threshold = defaultLongContextThresholdBPS
	}
	if threshold < 1 || threshold > 10_000 {
		return RiskPolicyRuntime{}, invalidAuto(
			"long-context threshold must be between 1 and 10000 basis points",
		)
	}
	return RiskPolicyRuntime{
		Version:                  RiskPolicyVersion2,
		SensitiveTextPatterns:    textPatterns,
		SensitiveToolPatterns:    toolPatterns,
		StructuredOutputHighRisk: config.StructuredOutputHighRisk,
		LongContextThresholdBPS:  threshold,
	}, nil
}

func resolveRiskPatterns(configured, defaults []string, label string) ([]string, error) {
	if configured == nil {
		return append([]string(nil), defaults...), nil
	}
	if len(configured) > maxRiskPatterns {
		return nil, invalidAuto("%s patterns must contain at most %d entries", label, maxRiskPatterns)
	}
	resolved := make([]string, len(configured))
	seen := make(map[string]struct{}, len(configured))
	for index, pattern := range configured {
		if pattern == "" || strings.TrimSpace(pattern) != pattern {
			return nil, invalidAuto("%s pattern %d must be non-empty without surrounding whitespace", label, index+1)
		}
		if utf8.RuneCountInString(pattern) > maxRiskPatternRunes {
			return nil, invalidAuto("%s pattern %d must contain at most %d characters", label, index+1, maxRiskPatternRunes)
		}
		key := strings.ToLower(pattern)
		if _, ok := seen[key]; ok {
			return nil, invalidAuto("%s pattern %d is a duplicate ignoring case", label, index+1)
		}
		seen[key] = struct{}{}
		resolved[index] = pattern
	}
	return resolved, nil
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
	if len(configured) < 2 {
		return nil, nil, invalidAuto("at least two participants are required")
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

	taskRoutes := make(map[TaskRouteKey]string, len(config.TaskRoutes))
	for _, configured := range config.TaskRoutes {
		if configured.TaskType != "" && !routingIDPattern.MatchString(configured.TaskType) {
			return RoutingStrategyRuntime{}, invalidAuto("task type %q is invalid", configured.TaskType)
		}
		if !validTaskRouteDifficulty(configured.Difficulty) {
			return RoutingStrategyRuntime{}, invalidAuto("task route difficulty %q is invalid", configured.Difficulty)
		}
		if configured.TaskType == "" && configured.Difficulty == "" {
			return RoutingStrategyRuntime{}, invalidAuto("task route requires a task type or difficulty")
		}
		key := TaskRouteKey{TaskType: configured.TaskType, Difficulty: configured.Difficulty}
		if _, duplicate := taskRoutes[key]; duplicate {
			return RoutingStrategyRuntime{}, invalidAuto(
				"task route %q/%q is duplicated", configured.TaskType, configured.Difficulty,
			)
		}
		if _, ok := routes[configured.Route]; !ok {
			return RoutingStrategyRuntime{}, invalidAuto(
				"task route %q/%q references unknown route %q",
				configured.TaskType, configured.Difficulty, configured.Route,
			)
		}
		taskRoutes[key] = configured.Route
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

func validTaskRouteDifficulty(value string) bool {
	switch value {
	case "", "easy", "medium", "hard":
		return true
	default:
		return false
	}
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
