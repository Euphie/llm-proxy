package routing

import "testing"

func TestSessionLockPolicyUsesConfiguredTokenThreshold(t *testing.T) {
	policy := SessionLockPolicy{TokenThreshold: 100_000}
	binding := SessionBinding{
		StableTaskCount:     3,
		StableConfidenceBPS: 9000,
		ConversationTokens:  99_999,
	}
	if got := policy.Apply(binding); got.ModelLocked {
		t.Fatalf("locked below configured threshold: %+v", got)
	}
	binding.ConversationTokens = 100_000
	if got := policy.Apply(binding); !got.ModelLocked || got.LockReason != SessionLockReasonStableConversation {
		t.Fatalf("binding=%+v", got)
	}
}

func TestSessionLockPolicyCountsCacheReadTokens(t *testing.T) {
	got := (SessionLockPolicy{TokenThreshold: 100_000}).Apply(SessionBinding{
		StableTaskCount:     3,
		StableConfidenceBPS: 9000,
		CacheReadTokens:     100_000,
	})
	if !got.ModelLocked || got.LockReason != SessionLockReasonStableConversation {
		t.Fatalf("binding=%+v", got)
	}
}

func TestSessionLockPolicyAlwaysLocksHighestModel(t *testing.T) {
	got := (SessionLockPolicy{TokenThreshold: 1_000_000}).Apply(SessionBinding{HighestModel: true})
	if !got.ModelLocked || got.LockReason != SessionLockReasonHighestModel {
		t.Fatalf("binding=%+v", got)
	}
}
