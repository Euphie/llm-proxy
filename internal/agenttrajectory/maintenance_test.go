package agenttrajectory

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Euphie/llm-proxy/internal/routing"
)

func TestMaintenanceSweepTimesOutIdleSessionsAndDeletesExpiredRecords(t *testing.T) {
	ctx := context.Background()
	collector, store, cipher, clock, closeStore := newCollectorFixture(t)
	defer closeStore()

	key := routing.SessionKey(bytes.Repeat([]byte{11}, 32))
	waiting := trajectoryObservation(routing.OperationOpenAIResponses, "model-a")
	waiting.SessionKey = &key
	waiting.RequestBody = []byte(`{"input":"check"}`)
	waiting.ResponseBody = []byte(`{"status":"completed","output":[{"type":"function_call","call_id":"c1","name":"lookup","arguments":"{}"}]}`)
	if _, ok, err := collector.Observe(ctx, waiting); err != nil || ok {
		t.Fatalf("waiting ok=%v err=%v", ok, err)
	}

	expired, err := store.Create(ctx, CreateInput{
		ProfileID: 1, ProfileSlug: "test", Protocol: routing.OperationAnthropicMessages,
		Source: SourceRequestSnapshot, Strategy: "active", Route: "agent",
		TaskType: "tool_use", Difficulty: "medium", Risk: "normal", VisionMode: "none",
		ModelPath: []string{"model-a"}, Payload: sealFixture(t, cipher, "expired"),
		StartedAt: clock.Add(-2 * time.Hour), ExpiresAt: clock.Add(-time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}

	*clock = clock.Add(31 * time.Minute)
	maintenance := NewMaintenance(store, func() time.Time { return *clock })
	if err := maintenance.Sweep(ctx); err != nil {
		t.Fatal(err)
	}

	listed, err := store.List(ctx, ListFilter{Status: StatusTimedOut, Limit: 10})
	if err != nil || listed.Total != 1 || listed.Items[0].ReasonCode != "idle_timeout" {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
	if _, err := store.Get(ctx, expired.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired Get err=%v", err)
	}
}

func TestMaintenanceContinuesSweepingWhileServiceIsIdle(t *testing.T) {
	ctx := context.Background()
	collector, store, _, clock, closeStore := newCollectorFixture(t)
	defer closeStore()

	key := routing.SessionKey(bytes.Repeat([]byte{12}, 32))
	waiting := trajectoryObservation(routing.OperationOpenAIResponses, "model-a")
	waiting.SessionKey = &key
	waiting.RequestBody = []byte(`{"input":"check"}`)
	waiting.ResponseBody = []byte(`{"status":"completed","output":[{"type":"function_call","call_id":"c1","name":"lookup","arguments":"{}"}]}`)
	if _, ok, err := collector.Observe(ctx, waiting); err != nil || ok {
		t.Fatalf("waiting ok=%v err=%v", ok, err)
	}

	maintenance := NewMaintenance(store, func() time.Time { return clock.Add(31 * time.Minute) })
	maintenance.Start(5 * time.Millisecond)
	defer maintenance.Close()

	deadline := time.Now().Add(time.Second)
	for {
		listed, err := store.List(ctx, ListFilter{Status: StatusTimedOut, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if listed.Total == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("idle trajectory was not timed out by periodic maintenance")
		}
		time.Sleep(time.Millisecond)
	}
}
