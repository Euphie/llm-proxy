package routing

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDeriveDifficultyUsesExplicitComplexitySignals(t *testing.T) {
	tests := []struct {
		name           string
		signals        ComplexitySignals
		underspecified bool
		want           Difficulty
		wantReason     string
	}{
		{
			name:       "obvious localized change is easy",
			signals:    ComplexitySignals{ObviousSolution: true, LocalizedChange: true},
			want:       DifficultyEasy,
			wantReason: "complexity_easy_localized",
		},
		{
			name:           "underspecified bug is medium",
			underspecified: true,
			want:           DifficultyMedium,
			wantReason:     "complexity_underspecified",
		},
		{
			name:       "formal proof is hard",
			signals:    ComplexitySignals{FormalProof: true},
			want:       DifficultyHard,
			wantReason: "complexity_formal_proof",
		},
		{
			name: "distributed invariants are hard",
			signals: ComplexitySignals{
				DistributedOrConcurrent:   true,
				InvariantsOrCompatibility: true,
			},
			want:       DifficultyHard,
			wantReason: "complexity_distributed_invariants",
		},
		{
			name: "cross-system unknown cause is hard",
			signals: ComplexitySignals{
				UnknownOrNondeterministic: true,
				CrossSystemOrLayer:        true,
			},
			want:       DifficultyHard,
			wantReason: "complexity_unknown_cross_system",
		},
		{
			name: "migration with interacting constraints is hard",
			signals: ComplexitySignals{
				MultipleInteractingConstraints: true,
				MigrationOrRollback:            true,
			},
			want:       DifficultyHard,
			wantReason: "complexity_interacting_constraints",
		},
		{
			name:       "bounded familiar work is medium",
			signals:    ComplexitySignals{BoundedFamiliarSteps: true},
			want:       DifficultyMedium,
			wantReason: "complexity_bounded",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reasons := deriveDifficulty(tt.signals, tt.underspecified)
			if got != tt.want || !slices.Contains(reasons, tt.wantReason) {
				t.Fatalf("difficulty=%q reasons=%v", got, reasons)
			}
		})
	}
}

func TestAnalyzerClassificationContractUsesFixedTaskTaxonomyAndPerDimensionConfidence(t *testing.T) {
	config := routingConfig(false)
	config.AutoRouting.Strategy.TaskRoutes = config.AutoRouting.Strategy.TaskRoutes[:1]
	analyzer := newAnalyzer(resolveRoutingRuntime(t, config), nil)
	wantTasks := []string{"coding", "general", "math", "reasoning", "simple", "tool_use", "vision"}
	for _, taskType := range wantTasks {
		if _, ok := analyzer.allowedTasks[taskType]; !ok {
			t.Fatalf("allowed tasks=%v, missing %q", analyzer.allowedTasks, taskType)
		}
	}
	if _, ok := analyzer.allowedTasks["default"]; ok {
		t.Fatalf("default must be a router fallback, allowed tasks=%v", analyzer.allowedTasks)
	}

	classification, err := analyzer.parseClassification(`{
		"task_type":"coding",
		"task_type_confidence_bps":9200,
		"risk":"normal",
		"risk_confidence_bps":9800,
		"complexity_confidence_bps":9100,
		"underspecified":false,
		"complexity_signals":{
			"obvious_solution":false,
			"localized_change":false,
			"bounded_familiar_steps":false,
			"formal_proof":false,
			"multiple_interacting_constraints":true,
			"distributed_or_concurrent":true,
			"unknown_or_nondeterministic":false,
			"cross_system_or_layer":true,
			"invariants_or_compatibility":true,
			"security_or_safety_critical":false,
			"quantitative_slo":false,
			"broad_verification":true,
			"migration_or_rollback":true
		}
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if classification.TaskType != "coding" || classification.Difficulty != DifficultyHard ||
		classification.TaskTypeConfidenceBPS != 9200 || classification.DifficultyConfidenceBPS != 9100 ||
		classification.RiskConfidenceBPS != 9800 || classification.ConfidenceBPS != 9100 {
		t.Fatalf("classification=%+v", classification)
	}
}

func TestAnalyzerInstructionsDefineTaskTypeOverlapByPrimaryDeliverable(t *testing.T) {
	for _, fragment := range []string{
		"Task type rubric:",
		"primary deliverable",
		"Analyzing or explaining an existing repository",
		"Merely exposing tools",
		"incidental screenshot",
		"Do not infer difficulty directly",
	} {
		if !strings.Contains(analyzerInstructions, fragment) {
			t.Fatalf("analyzer instructions missing %q", fragment)
		}
	}
}

func TestAnalyzerRejectsIncompleteSemanticAssessment(t *testing.T) {
	config := routingConfig(false)
	analyzer := newAnalyzer(resolveRoutingRuntime(t, config), nil)
	for index, payload := range []string{
		`{"task_type":"coding","risk":"normal","risk_confidence_bps":9000,"complexity_confidence_bps":9000,"underspecified":false,"complexity_signals":{}}`,
		`{"task_type":"coding","task_type_confidence_bps":9000,"risk":"normal","risk_confidence_bps":9000,"complexity_confidence_bps":9000,"underspecified":false,"complexity_signals":{}}`,
	} {
		_, err := analyzer.parseClassification(payload)
		if err == nil {
			t.Fatalf("accepted incomplete assessment: %s", payload)
		}
		wantCode := "invalid_confidence"
		if index == 1 {
			wantCode = "incomplete_complexity_signals"
		}
		if got := analyzerResultErrorCode(err); got != wantCode {
			t.Fatalf("error code=%q want=%q error=%v", got, wantCode, err)
		}
	}
}

func TestClassificationRegressionMatrix(t *testing.T) {
	config := routingConfig(false)
	analyzer := newAnalyzer(resolveRoutingRuntime(t, config), nil)
	tests := []struct {
		name       string
		assessment analyzerAssessment
		wantType   string
		wantDiff   Difficulty
		wantRisk   Risk
	}{
		{
			name: "screenshot guided code fix remains coding",
			assessment: analyzerAssessmentForTest(
				"coding", DifficultyMedium, RiskNormal, 9300,
			),
			wantType: "coding", wantDiff: DifficultyMedium, wantRisk: RiskNormal,
		},
		{
			name: "formal proof is math and hard",
			assessment: analyzerAssessmentForTest(
				"math", DifficultyHard, RiskNormal, 9500,
			),
			wantType: "math", wantDiff: DifficultyHard, wantRisk: RiskNormal,
		},
		{
			name: "distributed migration proof is reasoning and hard",
			assessment: analyzerAssessment{
				TaskType: "reasoning", TaskTypeConfidenceBPS: 9600,
				Risk: RiskNormal, RiskConfidenceBPS: 9300, ComplexityConfidenceBPS: 9500,
				Signals: ComplexitySignals{
					MultipleInteractingConstraints: true,
					DistributedOrConcurrent:        true,
					InvariantsOrCompatibility:      true,
					MigrationOrRollback:            true,
				},
			},
			wantType: "reasoning", wantDiff: DifficultyHard, wantRisk: RiskNormal,
		},
		{
			name: "repository wide analysis is coding and hard",
			assessment: analyzerAssessment{
				TaskType: "coding", TaskTypeConfidenceBPS: 9400,
				Risk: RiskNormal, RiskConfidenceBPS: 9500, ComplexityConfidenceBPS: 9200,
				Signals: ComplexitySignals{
					CrossSystemOrLayer: true,
					BroadVerification:  true,
				},
			},
			wantType: "coding", wantDiff: DifficultyHard, wantRisk: RiskNormal,
		},
		{
			name: "external orchestration is tool use",
			assessment: analyzerAssessmentForTest(
				"tool_use", DifficultyMedium, RiskNormal, 9200,
			),
			wantType: "tool_use", wantDiff: DifficultyMedium, wantRisk: RiskNormal,
		},
		{
			name: "essential image interpretation is vision",
			assessment: analyzerAssessmentForTest(
				"vision", DifficultyMedium, RiskNormal, 9400,
			),
			wantType: "vision", wantDiff: DifficultyMedium, wantRisk: RiskNormal,
		},
		{
			name: "underspecified bug stays medium",
			assessment: analyzerAssessment{
				TaskType: "coding", TaskTypeConfidenceBPS: 9000,
				Risk: RiskNormal, RiskConfidenceBPS: 9400,
				ComplexityConfidenceBPS: 6200, Underspecified: true,
			},
			wantType: "coding", wantDiff: DifficultyMedium, wantRisk: RiskNormal,
		},
		{
			name: "simple destructive action is high risk",
			assessment: analyzerAssessmentForTest(
				"simple", DifficultyEasy, RiskHigh, 9700,
			),
			wantType: "simple", wantDiff: DifficultyEasy, wantRisk: RiskHigh,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			classification, err := analyzer.parseClassification(
				analyzerClassificationJSON(t, tt.assessment),
			)
			if err != nil || classification.TaskType != tt.wantType ||
				classification.Difficulty != tt.wantDiff || classification.Risk != tt.wantRisk {
				t.Fatalf("classification=%+v err=%v", classification, err)
			}
		})
	}
}

func TestApplyClassificationConfidencePolicyHandlesDimensionsIndependently(t *testing.T) {
	base := Classification{
		TaskType: "coding", Difficulty: DifficultyMedium, Risk: RiskNormal,
		TaskTypeConfidenceBPS: 9000, DifficultyConfidenceBPS: 9000,
		RiskConfidenceBPS: 9000, ConfidenceBPS: 9000,
		Source: ClassificationSourceAnalyzer,
	}
	tests := []struct {
		name       string
		mutate     func(*Classification)
		wantType   string
		wantDiff   Difficulty
		wantRisk   Risk
		wantReason string
	}{
		{
			name: "low task confidence uses default route only",
			mutate: func(classification *Classification) {
				classification.TaskTypeConfidenceBPS = 6000
			},
			wantType: "default", wantDiff: DifficultyMedium, wantRisk: RiskNormal,
			wantReason: "task_type_low_confidence",
		},
		{
			name: "low complexity confidence keeps non-hard work medium",
			mutate: func(classification *Classification) {
				classification.Difficulty = DifficultyEasy
				classification.DifficultyConfidenceBPS = 6000
			},
			wantType: "coding", wantDiff: DifficultyMedium, wantRisk: RiskNormal,
			wantReason: "difficulty_low_confidence",
		},
		{
			name: "low complexity confidence preserves deterministic hard guard",
			mutate: func(classification *Classification) {
				classification.Difficulty = DifficultyHard
				classification.DifficultyConfidenceBPS = 6000
			},
			wantType: "coding", wantDiff: DifficultyHard, wantRisk: RiskNormal,
			wantReason: "difficulty_low_confidence_hard_guard",
		},
		{
			name: "low risk confidence becomes unknown",
			mutate: func(classification *Classification) {
				classification.RiskConfidenceBPS = 6000
			},
			wantType: "coding", wantDiff: DifficultyMedium, wantRisk: RiskUnknown,
			wantReason: "risk_low_confidence",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			classification := base
			tt.mutate(&classification)
			got := applyClassificationConfidencePolicy(classification, 7000)
			if got.TaskType != tt.wantType || got.Difficulty != tt.wantDiff || got.Risk != tt.wantRisk ||
				!slices.Contains(got.ReasonCodes, tt.wantReason) {
				t.Fatalf("classification=%+v", got)
			}
		})
	}
}

func TestEngineAnalyzesFormerSimplePhraseInsteadOfUsingTextWhitelist(t *testing.T) {
	var analyzerCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		analyzerCalls.Add(1)
		_, _ = io.WriteString(w, anthropicClassificationResponse(t, analyzerAssessment{
			TaskType: "simple", TaskTypeConfidenceBPS: 9800,
			Risk: RiskNormal, RiskConfidenceBPS: 9900,
			ComplexityConfidenceBPS: 9700,
			Signals:                 ComplexitySignals{ObviousSolution: true},
		}))
	}))
	defer server.Close()

	config := routingConfig(false)
	config.Upstream = server.URL
	engine, err := NewEngine(resolveRoutingRuntime(t, config), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	budget, ctx, cancel := engine.NewAttemptBudget(context.Background())
	defer cancel()
	plan, classification, err := engine.Route(
		ctx, nil,
		autoAnthropicRequest(t, `{"model":"auto","max_tokens":1000,"messages":[{"role":"user","content":"你好"}]}`),
		budget,
	)
	if err != nil {
		t.Fatal(err)
	}
	if analyzerCalls.Load() != 1 || classification.Source != ClassificationSourceAnalyzer ||
		classification.TaskType != "simple" || classification.Difficulty != DifficultyEasy || plan.Model() != "fast" {
		t.Fatalf("calls=%d classification=%+v plan=%+v", analyzerCalls.Load(), classification, plan.Snapshot())
	}
}

type analyzerAssessment struct {
	TaskType                string
	TaskTypeConfidenceBPS   int
	Risk                    Risk
	RiskConfidenceBPS       int
	ComplexityConfidenceBPS int
	Underspecified          bool
	Signals                 ComplexitySignals
}

func analyzerAssessmentForTest(
	taskType string,
	difficulty Difficulty,
	risk Risk,
	confidence int,
) analyzerAssessment {
	signals := ComplexitySignals{BoundedFamiliarSteps: true}
	switch difficulty {
	case DifficultyEasy:
		signals = ComplexitySignals{ObviousSolution: true, LocalizedChange: true}
	case DifficultyHard:
		signals = ComplexitySignals{FormalProof: true}
	}
	return analyzerAssessment{
		TaskType: taskType, TaskTypeConfidenceBPS: confidence,
		Risk: risk, RiskConfidenceBPS: confidence,
		ComplexityConfidenceBPS: confidence, Signals: signals,
	}
}

func analyzerClassificationJSON(t *testing.T, assessment analyzerAssessment) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"task_type":                 assessment.TaskType,
		"task_type_confidence_bps":  assessment.TaskTypeConfidenceBPS,
		"risk":                      assessment.Risk,
		"risk_confidence_bps":       assessment.RiskConfidenceBPS,
		"complexity_confidence_bps": assessment.ComplexityConfidenceBPS,
		"underspecified":            assessment.Underspecified,
		"complexity_signals":        assessment.Signals,
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(payload)
}

func anthropicClassificationResponse(t *testing.T, assessment analyzerAssessment) string {
	t.Helper()
	payload := analyzerClassificationJSON(t, assessment)
	body, err := json.Marshal(map[string]any{
		"content": []map[string]string{{"type": "text", "text": payload}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func anthropicClassificationResponseWithUsage(
	t *testing.T,
	assessment analyzerAssessment,
	inputTokens int,
	outputTokens int,
) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"content": []map[string]string{{
			"type": "text", "text": analyzerClassificationJSON(t, assessment),
		}},
		"usage": map[string]int{"input_tokens": inputTokens, "output_tokens": outputTokens},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}
