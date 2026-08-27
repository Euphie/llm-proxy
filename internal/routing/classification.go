package routing

import "slices"

var complexitySignalNames = []string{
	"obvious_solution", "localized_change", "bounded_familiar_steps", "formal_proof",
	"multiple_interacting_constraints", "distributed_or_concurrent",
	"unknown_or_nondeterministic", "cross_system_or_layer", "invariants_or_compatibility",
	"security_or_safety_critical", "quantitative_slo", "broad_verification",
	"migration_or_rollback",
}

type ComplexitySignals struct {
	ObviousSolution                bool `json:"obvious_solution"`
	LocalizedChange                bool `json:"localized_change"`
	BoundedFamiliarSteps           bool `json:"bounded_familiar_steps"`
	FormalProof                    bool `json:"formal_proof"`
	MultipleInteractingConstraints bool `json:"multiple_interacting_constraints"`
	DistributedOrConcurrent        bool `json:"distributed_or_concurrent"`
	UnknownOrNondeterministic      bool `json:"unknown_or_nondeterministic"`
	CrossSystemOrLayer             bool `json:"cross_system_or_layer"`
	InvariantsOrCompatibility      bool `json:"invariants_or_compatibility"`
	SecurityOrSafetyCritical       bool `json:"security_or_safety_critical"`
	QuantitativeSLO                bool `json:"quantitative_slo"`
	BroadVerification              bool `json:"broad_verification"`
	MigrationOrRollback            bool `json:"migration_or_rollback"`
}

func (signals ComplexitySignals) Map() map[string]bool {
	return map[string]bool{
		"obvious_solution":                 signals.ObviousSolution,
		"localized_change":                 signals.LocalizedChange,
		"bounded_familiar_steps":           signals.BoundedFamiliarSteps,
		"formal_proof":                     signals.FormalProof,
		"multiple_interacting_constraints": signals.MultipleInteractingConstraints,
		"distributed_or_concurrent":        signals.DistributedOrConcurrent,
		"unknown_or_nondeterministic":      signals.UnknownOrNondeterministic,
		"cross_system_or_layer":            signals.CrossSystemOrLayer,
		"invariants_or_compatibility":      signals.InvariantsOrCompatibility,
		"security_or_safety_critical":      signals.SecurityOrSafetyCritical,
		"quantitative_slo":                 signals.QuantitativeSLO,
		"broad_verification":               signals.BroadVerification,
		"migration_or_rollback":            signals.MigrationOrRollback,
	}
}

func deriveDifficulty(signals ComplexitySignals, underspecified bool) (Difficulty, []string) {
	switch {
	case signals.FormalProof:
		return DifficultyHard, []string{"complexity_formal_proof"}
	case signals.DistributedOrConcurrent && signals.InvariantsOrCompatibility:
		return DifficultyHard, []string{"complexity_distributed_invariants"}
	case signals.DistributedOrConcurrent &&
		(signals.UnknownOrNondeterministic || signals.MultipleInteractingConstraints):
		return DifficultyHard, []string{"complexity_distributed_interactions"}
	case signals.UnknownOrNondeterministic && signals.CrossSystemOrLayer:
		return DifficultyHard, []string{"complexity_unknown_cross_system"}
	case signals.BroadVerification && signals.CrossSystemOrLayer:
		return DifficultyHard, []string{"complexity_cross_system_verification"}
	case signals.MultipleInteractingConstraints &&
		(signals.CrossSystemOrLayer || signals.InvariantsOrCompatibility ||
			signals.MigrationOrRollback || signals.BroadVerification):
		return DifficultyHard, []string{"complexity_interacting_constraints"}
	case signals.QuantitativeSLO &&
		(signals.MultipleInteractingConstraints || signals.BroadVerification):
		return DifficultyHard, []string{"complexity_quantitative_slo"}
	case signals.SecurityOrSafetyCritical &&
		(signals.CrossSystemOrLayer || signals.MultipleInteractingConstraints || signals.BroadVerification):
		return DifficultyHard, []string{"complexity_security_verification"}
	case underspecified:
		return DifficultyMedium, []string{"complexity_underspecified"}
	case signals.ObviousSolution && signals.LocalizedChange:
		return DifficultyEasy, []string{"complexity_easy_localized"}
	case signals.ObviousSolution && !signals.BoundedFamiliarSteps:
		return DifficultyEasy, []string{"complexity_easy_obvious"}
	case signals.BoundedFamiliarSteps:
		return DifficultyMedium, []string{"complexity_bounded"}
	default:
		return DifficultyMedium, []string{"complexity_medium_default"}
	}
}

func minimumConfidence(values ...int) int {
	minimum := 10_000
	for _, value := range values {
		if value < minimum {
			minimum = value
		}
	}
	return minimum
}

func applyClassificationConfidencePolicy(
	classification Classification,
	minimum int,
) Classification {
	if classification.TaskTypeConfidenceBPS < minimum {
		classification.TaskType = "default"
		classification.ReasonCodes = appendUniqueReason(
			classification.ReasonCodes, "task_type_low_confidence",
		)
	}
	if classification.DifficultyConfidenceBPS < minimum {
		if classification.Difficulty == DifficultyHard {
			classification.ReasonCodes = appendUniqueReason(
				classification.ReasonCodes, "difficulty_low_confidence_hard_guard",
			)
		} else {
			classification.Difficulty = DifficultyMedium
			classification.ReasonCodes = appendUniqueReason(
				classification.ReasonCodes, "difficulty_low_confidence",
			)
		}
	}
	if classification.RiskConfidenceBPS < minimum {
		classification.Risk = RiskUnknown
		classification.ReasonCodes = appendUniqueReason(
			classification.ReasonCodes, "risk_low_confidence",
		)
	}
	classification.ConfidenceBPS = minimumConfidence(
		classification.TaskTypeConfidenceBPS,
		classification.DifficultyConfidenceBPS,
		classification.RiskConfidenceBPS,
	)
	return classification
}

func appendUniqueReason(reasons []string, reason string) []string {
	if slices.Contains(reasons, reason) {
		return reasons
	}
	return append(reasons, reason)
}
