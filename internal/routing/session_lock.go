package routing

const (
	SessionLockReasonHighestModel       = "highest_model"
	SessionLockReasonStableConversation = "stable_session_conversation_v2"

	sessionLockTaskThreshold       = 3
	sessionLockConfidenceThreshold = 8500
)

type SessionLockPolicy struct {
	TokenThreshold int
}

func (p SessionLockPolicy) Apply(binding SessionBinding) SessionBinding {
	switch {
	case binding.HighestModel:
		binding.ModelLocked = true
		binding.LockReason = SessionLockReasonHighestModel
	case p.TokenThreshold > 0 &&
		binding.StableTaskCount >= sessionLockTaskThreshold &&
		binding.StableConfidenceBPS >= sessionLockConfidenceThreshold &&
		(binding.ConversationTokens >= p.TokenThreshold || binding.CacheReadTokens >= p.TokenThreshold):
		binding.ModelLocked = true
		binding.LockReason = SessionLockReasonStableConversation
	default:
		binding.ModelLocked = false
		binding.LockReason = ""
	}
	return binding
}

func stableSessionClassification(binding SessionBinding) bool {
	return binding.ClassificationReliable &&
		binding.ClassificationConfidenceBPS >= sessionLockConfidenceThreshold &&
		hasUsableSessionClassification(binding)
}
