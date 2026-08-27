package agenttrajectory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Euphie/llm-proxy/internal/database"
	"github.com/Euphie/llm-proxy/internal/routing"
)

func TestCollectorAssemblesMultiTurnSessionWithoutDuplicates(t *testing.T) {
	ctx := context.Background()
	collector, store, cipher, clock, closeStore := newCollectorFixture(t)
	defer closeStore()
	key := routing.SessionKey(bytes.Repeat([]byte{3}, 32))

	first := trajectoryObservation(routing.OperationAnthropicMessages, "model-a")
	first.SessionKey = &key
	first.RequestBody = []byte(`{"messages":[{"role":"user","content":"check weather"}]}`)
	first.ResponseBody = []byte(`{"content":[{"type":"tool_use","id":"toolu_1","name":"weather","input":{"city":"Paris"}}],"stop_reason":"tool_use"}`)
	if completed, ok, err := collector.Observe(ctx, first); err != nil || ok || completed.Record.ID != 0 {
		t.Fatalf("first completed=%+v ok=%v err=%v", completed, ok, err)
	}
	active, found, err := store.FindCollectingBySession(ctx, 1, key[:])
	if err != nil || !found || active.Status != StatusCollecting || active.Turns != 1 {
		t.Fatalf("active=%+v found=%v err=%v", active, found, err)
	}

	*clock = clock.Add(time.Second)
	second := trajectoryObservation(routing.OperationAnthropicMessages, "model-a")
	second.SessionKey = &key
	second.RequestBody = []byte(`{"messages":[
		{"role":"user","content":"check weather"},
		{"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"weather","input":{"city":"Paris"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"sunny"}]}
	]}`)
	second.ResponseBody = []byte(`{"content":[{"type":"text","text":"Paris is sunny."}],"stop_reason":"end_turn"}`)
	completed, ok, err := collector.Observe(ctx, second)
	if err != nil || !ok {
		t.Fatalf("second completed=%+v ok=%v err=%v", completed, ok, err)
	}
	if !completed.Eligible || completed.Record.Status != StatusCompleted ||
		completed.Record.ToolCalls != 1 || completed.Record.Turns != 2 ||
		len(completed.Record.ModelPath) != 1 || completed.Record.ModelPath[0] != "model-a" {
		t.Fatalf("completed=%+v", completed)
	}
	opened, err := cipher.Open(completed.Record.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(opened.Events) != 4 || opened.Events[0].Kind != EventUserMessage ||
		opened.Events[1].Kind != EventToolCall || opened.Events[2].Kind != EventToolResult ||
		opened.Events[3].Text != "Paris is sunny." {
		t.Fatalf("events=%+v", opened.Events)
	}

	restarted := NewCollector(store, cipher, func() time.Time { return *clock })
	replayed, replayedOK, err := restarted.Observe(ctx, second)
	if err != nil || !replayedOK || replayed.Record.ID != completed.Record.ID {
		t.Fatalf("replayed=%+v ok=%v err=%v", replayed, replayedOK, err)
	}
	if replayed.Eligible {
		t.Fatalf("replayed trajectory became eligible again: %+v", replayed)
	}
	listed, err := store.List(ctx, ListFilter{ProfileID: 1, Limit: 10})
	if err != nil || listed.Total != 1 {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
}

func TestCollectorSerializesConcurrentObservationsForOneSession(t *testing.T) {
	ctx := context.Background()
	collector, store, _, _, closeStore := newCollectorFixture(t)
	defer closeStore()
	key := routing.SessionKey(bytes.Repeat([]byte{13}, 32))

	first := trajectoryObservation(routing.OperationAnthropicMessages, "model-initial")
	first.SessionKey = &key
	first.CandidateCostMicroUSD = 1
	first.RequestBody = []byte(`{"messages":[{"role":"user","content":"check"}]}`)
	first.ResponseBody = []byte(`{"content":[{"type":"tool_use","id":"initial","name":"lookup","input":{}}],"stop_reason":"tool_use"}`)
	if _, ok, err := collector.Observe(ctx, first); err != nil || ok {
		t.Fatalf("first ok=%v err=%v", ok, err)
	}

	const observations = 32
	start := make(chan struct{})
	errs := make(chan error, observations)
	var workers sync.WaitGroup
	for index := range observations {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			observation := trajectoryObservation(routing.OperationAnthropicMessages, fmt.Sprintf("model-%02d", index))
			observation.SessionKey = &key
			observation.CandidateCostMicroUSD = 1
			observation.RequestBody = []byte(`{"messages":[{"role":"user","content":"check"}]}`)
			observation.ResponseBody = []byte(fmt.Sprintf(
				`{"content":[{"type":"tool_use","id":"call-%02d","name":"lookup","input":{}}],"stop_reason":"tool_use"}`,
				index,
			))
			if _, ok, err := collector.Observe(ctx, observation); err != nil || ok {
				errs <- fmt.Errorf("observation %d ok=%v err=%w", index, ok, err)
			}
		}()
	}
	close(start)
	workers.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	active, found, err := store.FindCollectingBySession(ctx, 1, key[:])
	if err != nil || !found {
		t.Fatalf("active=%+v found=%v err=%v", active, found, err)
	}
	if active.CandidateCostMicroUSD != observations+1 || len(active.ModelPath) != observations+1 ||
		active.ToolCalls != observations+1 {
		t.Fatalf("concurrent state lost updates: cost=%d models=%d tool_calls=%d", active.CandidateCostMicroUSD, len(active.ModelPath), active.ToolCalls)
	}
}

func TestCollectorIgnoresStaleWaitingObservationMetadata(t *testing.T) {
	ctx := context.Background()
	collector, store, _, _, closeStore := newCollectorFixture(t)
	defer closeStore()
	key := routing.SessionKey(bytes.Repeat([]byte{14}, 32))

	first := trajectoryObservation(routing.OperationAnthropicMessages, "model-a")
	first.SessionKey = &key
	first.CandidateCostMicroUSD = 10
	first.RequestBody = []byte(`{"messages":[{"role":"user","content":"check"}]}`)
	first.ResponseBody = []byte(`{"content":[{"type":"tool_use","id":"call-a","name":"lookup","input":{}}],"stop_reason":"tool_use"}`)
	if _, ok, err := collector.Observe(ctx, first); err != nil || ok {
		t.Fatalf("first ok=%v err=%v", ok, err)
	}

	second := trajectoryObservation(routing.OperationAnthropicMessages, "model-b")
	second.SessionKey = &key
	second.CandidateCostMicroUSD = 20
	second.RequestBody = []byte(`{"messages":[{"role":"user","content":"check"},{"role":"assistant","content":[{"type":"tool_use","id":"call-a","name":"lookup","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-a","content":"ok"}]}]}`)
	second.ResponseBody = []byte(`{"content":[{"type":"tool_use","id":"call-b","name":"lookup","input":{}}],"stop_reason":"tool_use"}`)
	if _, ok, err := collector.Observe(ctx, second); err != nil || ok {
		t.Fatalf("second ok=%v err=%v", ok, err)
	}
	before, found, err := store.FindCollectingBySession(ctx, 1, key[:])
	if err != nil || !found {
		t.Fatalf("before=%+v found=%v err=%v", before, found, err)
	}

	if _, ok, err := collector.Observe(ctx, first); err != nil || ok {
		t.Fatalf("stale replay ok=%v err=%v", ok, err)
	}
	after, found, err := store.FindCollectingBySession(ctx, 1, key[:])
	if err != nil || !found {
		t.Fatalf("after=%+v found=%v err=%v", after, found, err)
	}
	if after.CandidateCostMicroUSD != before.CandidateCostMicroUSD ||
		!bytes.Equal(after.ObservationDigest, before.ObservationDigest) ||
		!slices.Equal(after.ModelPath, before.ModelPath) || after.ToolCalls != before.ToolCalls || after.Turns != before.Turns {
		t.Fatalf("stale replay changed state: before=%+v after=%+v", before, after)
	}
}

func TestCollectorBoundsFinalTextInsideEncryptedPlaintextLimit(t *testing.T) {
	ctx := context.Background()
	collector, _, cipher, _, closeStore := newCollectorFixture(t)
	defer closeStore()
	collector.maxPlaintext = 1024

	observation := completedSnapshotObservation("model-a")
	response, err := json.Marshal(map[string]any{
		"content":     []map[string]string{{"type": "text", "text": string(bytes.Repeat([]byte("x"), 4096))}},
		"stop_reason": "end_turn",
	})
	if err != nil {
		t.Fatal(err)
	}
	observation.ResponseBody = response
	completed, ok, err := collector.Observe(ctx, observation)
	if err != nil || !ok {
		t.Fatalf("completed=%+v ok=%v err=%v", completed, ok, err)
	}
	if completed.Record.Status != StatusSkipped || completed.Record.ReasonCode != "trajectory_truncated" || !completed.Record.Truncated {
		t.Fatalf("completed=%+v", completed)
	}
	opened, err := cipher.Open(completed.Record.Payload)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(opened)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > collector.maxPlaintext {
		t.Fatalf("plaintext bytes=%d limit=%d", len(encoded), collector.maxPlaintext)
	}
}

func TestCollectorUsesOnlyCompletedRequestSnapshotsWithoutSessionID(t *testing.T) {
	ctx := context.Background()
	collector, store, cipher, _, closeStore := newCollectorFixture(t)
	defer closeStore()

	waiting := trajectoryObservation(routing.OperationOpenAIChatCompletions, "model-a")
	waiting.RequestBody = []byte(`{"messages":[{"role":"user","content":"check weather"}]}`)
	waiting.ResponseBody = []byte(`{"choices":[{"message":{"tool_calls":[{"id":"call_1","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Paris\"}"}}]},"finish_reason":"tool_calls"}]}`)
	if _, ok, err := collector.Observe(ctx, waiting); err != nil || ok {
		t.Fatalf("waiting ok=%v err=%v", ok, err)
	}
	if listed, err := store.List(ctx, ListFilter{Limit: 10}); err != nil || listed.Total != 0 {
		t.Fatalf("waiting snapshot was persisted: %+v err=%v", listed, err)
	}

	final := trajectoryObservation(routing.OperationOpenAIChatCompletions, "model-a")
	final.RequestBody = []byte(`{"messages":[
		{"role":"user","content":"check weather"},
		{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Paris\"}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"sunny"}
	]}`)
	final.ResponseBody = []byte(`{"choices":[{"message":{"content":"Paris is sunny."},"finish_reason":"stop"}]}`)
	completed, ok, err := collector.Observe(ctx, final)
	if err != nil || !ok || !completed.Eligible || completed.Record.Source != SourceRequestSnapshot {
		t.Fatalf("completed=%+v ok=%v err=%v", completed, ok, err)
	}
	opened, err := cipher.Open(completed.Record.Payload)
	if err != nil || len(opened.Events) != 4 {
		t.Fatalf("opened=%+v err=%v", opened, err)
	}
}

func TestCollectorKeepsOnlyLatestUserTaskForEvaluation(t *testing.T) {
	ctx := context.Background()
	collector, _, _, _, closeStore := newCollectorFixture(t)
	defer closeStore()
	observation := trajectoryObservation(routing.OperationAnthropicMessages, "model-a")
	observation.RequestBody = []byte(`{"messages":[
		{"role":"user","content":"old task"},
		{"role":"assistant","content":[{"type":"text","text":"old answer"}]},
		{"role":"user","content":"verify the new deployment"},
		{"role":"assistant","content":[{"type":"tool_use","id":"t2","name":"verify","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":"all checks passed"}]}
	]}`)
	observation.ResponseBody = []byte(`{"content":[{"type":"text","text":"Deployment verification passed."}],"stop_reason":"end_turn"}`)
	completed, ok, err := collector.Observe(ctx, observation)
	if err != nil || !ok || !completed.Eligible {
		t.Fatalf("completed=%+v ok=%v err=%v", completed, ok, err)
	}
	if len(completed.Plaintext.Events) != 4 || completed.Plaintext.Events[0].Text != "verify the new deployment" {
		t.Fatalf("events=%+v", completed.Plaintext.Events)
	}
	for _, event := range completed.Plaintext.Events {
		if event.Text == "old task" || event.Text == "old answer" {
			t.Fatalf("old task leaked into evaluation: %+v", completed.Plaintext.Events)
		}
	}
}

func TestCollectorSkipsIncompleteQualityEvidence(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name       string
		request    string
		response   string
		wantReason string
	}{
		{
			name: "missing tool result",
			request: `{"messages":[{"role":"user","content":"verify deployment"},
				{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"verify","input":{}}]}]}`,
			response:   `{"content":[{"type":"text","text":"Deployment looks correct."}],"stop_reason":"end_turn"}`,
			wantReason: "incomplete_tool_evidence",
		},
		{
			name: "insufficient final answer",
			request: `{"messages":[{"role":"user","content":"verify deployment"},
				{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"verify","input":{}}]},
				{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"passed"}]}]}`,
			response:   `{"content":[{"type":"text","text":"done"}],"stop_reason":"end_turn"}`,
			wantReason: "insufficient_final_answer",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			collector, _, _, _, closeStore := newCollectorFixture(t)
			defer closeStore()
			observation := trajectoryObservation(routing.OperationAnthropicMessages, "model-a")
			observation.RequestBody = []byte(test.request)
			observation.ResponseBody = []byte(test.response)
			completed, ok, err := collector.Observe(ctx, observation)
			if err != nil || !ok || completed.Eligible || completed.Record.ReasonCode != test.wantReason {
				t.Fatalf("completed=%+v ok=%v err=%v", completed, ok, err)
			}
		})
	}
}

func TestCollectorClassifiesAuditOnlyTrajectories(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name       string
		configure  func(*Collector)
		observe    func(t *testing.T, collector *Collector) CompletedTrajectory
		wantReason string
	}{
		{
			name: "high risk",
			observe: func(t *testing.T, collector *Collector) CompletedTrajectory {
				t.Helper()
				observation := completedSnapshotObservation("model-a")
				observation.Risk = "high"
				completed, ok, err := collector.Observe(ctx, observation)
				if err != nil || !ok {
					t.Fatalf("ok=%v err=%v", ok, err)
				}
				return completed
			},
			wantReason: "high_risk",
		},
		{
			name:      "truncated",
			configure: func(collector *Collector) { collector.maxEvents = 2 },
			observe: func(t *testing.T, collector *Collector) CompletedTrajectory {
				t.Helper()
				completed, ok, err := collector.Observe(ctx, completedSnapshotObservation("model-a"))
				if err != nil || !ok {
					t.Fatalf("ok=%v err=%v", ok, err)
				}
				return completed
			},
			wantReason: "trajectory_truncated",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			collector, _, _, _, closeStore := newCollectorFixture(t)
			defer closeStore()
			if tt.configure != nil {
				tt.configure(collector)
			}
			completed := tt.observe(t, collector)
			if completed.Eligible || completed.Record.Status != StatusSkipped ||
				completed.Record.ReasonCode != tt.wantReason {
				t.Fatalf("completed=%+v", completed)
			}
		})
	}
}

func TestCollectorKeepsMixedModelTrajectoryEligibleWithEncryptedEscalationPath(t *testing.T) {
	ctx := context.Background()
	collector, _, cipher, _, closeStore := newCollectorFixture(t)
	defer closeStore()
	key := routing.SessionKey(bytes.Repeat([]byte{8}, 32))
	first := trajectoryObservation(routing.OperationAnthropicMessages, "model-a")
	first.SessionKey = &key
	first.RequestBody = []byte(`{"messages":[{"role":"user","content":"check"}]}`)
	first.ResponseBody = []byte(`{"content":[{"type":"tool_use","id":"t1","name":"lookup","input":{}}],"stop_reason":"tool_use"}`)
	if _, ok, err := collector.Observe(ctx, first); err != nil || ok {
		t.Fatalf("first ok=%v err=%v", ok, err)
	}
	second := trajectoryObservation(routing.OperationAnthropicMessages, "model-b")
	second.SessionKey = &key
	second.ModelPath = []string{"model-a", "model-b"}
	second.FinalOutputCostMicroUSD = 23
	second.FinalOutputLatencyMS = 45
	second.FinalOutputMetricsKnown = true
	second.SelfEscalations = []SelfEscalation{{
		FromModel: "model-a", ToModel: "model-b", Reason: "needs_stronger_reasoning",
	}}
	second.RequestBody = []byte(`{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"lookup","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}]}`)
	second.ResponseBody = []byte(`{"content":[{"type":"text","text":"Lookup completed successfully."}],"stop_reason":"end_turn"}`)
	completed, ok, err := collector.Observe(ctx, second)
	if err != nil || !ok || !completed.Eligible {
		t.Fatalf("completed=%+v ok=%v err=%v", completed, ok, err)
	}
	opened, err := cipher.Open(completed.Record.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(opened.ModelPath, []string{"model-a", "model-b"}) ||
		len(opened.SelfEscalations) != 1 || opened.SelfEscalations[0].Reason != "needs_stronger_reasoning" ||
		opened.FinalOutputCostMicroUSD != 23 || opened.FinalOutputLatencyMS != 45 || !opened.FinalOutputMetricsKnown {
		t.Fatalf("plaintext=%+v", opened)
	}
}

func TestCollectorTimesOutIdleSessionWithoutQualityEvidence(t *testing.T) {
	ctx := context.Background()
	collector, store, _, clock, closeStore := newCollectorFixture(t)
	defer closeStore()
	key := routing.SessionKey(bytes.Repeat([]byte{9}, 32))
	observation := trajectoryObservation(routing.OperationOpenAIResponses, "model-a")
	observation.SessionKey = &key
	observation.RequestBody = []byte(`{"input":"check"}`)
	observation.ResponseBody = []byte(`{"status":"completed","output":[{"type":"function_call","call_id":"c1","name":"lookup","arguments":"{}"}]}`)
	if _, ok, err := collector.Observe(ctx, observation); err != nil || ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	*clock = clock.Add(31 * time.Minute)
	count, err := collector.ExpireIdle(ctx)
	if err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	listed, err := store.List(ctx, ListFilter{Status: StatusTimedOut, Limit: 10})
	if err != nil || listed.Total != 1 || listed.Items[0].ReasonCode != "idle_timeout" || listed.Items[0].EvidenceRecorded {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
}

func TestCollectorDoesNotScanUnrelatedIdleSessionsPerObservation(t *testing.T) {
	ctx := context.Background()
	collector, store, _, clock, closeStore := newCollectorFixture(t)
	defer closeStore()
	key := routing.SessionKey(bytes.Repeat([]byte{10}, 32))
	waiting := trajectoryObservation(routing.OperationOpenAIResponses, "model-a")
	waiting.SessionKey = &key
	waiting.RequestBody = []byte(`{"input":"check"}`)
	waiting.ResponseBody = []byte(`{"status":"completed","output":[{"type":"function_call","call_id":"c1","name":"lookup","arguments":"{}"}]}`)
	if _, ok, err := collector.Observe(ctx, waiting); err != nil || ok {
		t.Fatalf("waiting ok=%v err=%v", ok, err)
	}

	*clock = clock.Add(31 * time.Minute)
	if _, ok, err := collector.Observe(ctx, completedSnapshotObservation("model-a")); err != nil || !ok {
		t.Fatalf("completed ok=%v err=%v", ok, err)
	}
	listed, err := store.List(ctx, ListFilter{Status: StatusTimedOut, Limit: 10})
	if err != nil || listed.Total != 0 {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
}

func newCollectorFixture(t *testing.T) (*Collector, *Store, *Cipher, *time.Time, func()) {
	t.Helper()
	dataDir := t.TempDir()
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 8, 7, 6, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`
		INSERT INTO profiles (id, slug, display_name, enabled, config_json, created_at, updated_at)
		VALUES (1, 'test', 'Test', 1, '{}', ?, ?)
	`, clock.Format(time.RFC3339Nano), clock.Format(time.RFC3339Nano)); err != nil {
		db.Close()
		t.Fatal(err)
	}
	cipher, err := OpenCipher(dataDir)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	store := NewStore(db, func() time.Time { return clock })
	collector := NewCollector(store, cipher, func() time.Time { return clock })
	return collector, store, cipher, &clock, func() { _ = db.Close() }
}

func trajectoryObservation(operation routing.Operation, model string) Observation {
	return Observation{
		ProfileID: 1, ProfileSlug: "test", Protocol: operation,
		Strategy: "active", Route: "agent", TaskType: "tool_use", Difficulty: "medium",
		Risk: "normal", VisionMode: "none", Model: model, LatencyMS: 500,
	}
}

func completedSnapshotObservation(model string) Observation {
	observation := trajectoryObservation(routing.OperationAnthropicMessages, model)
	observation.RequestBody = []byte(`{"messages":[
		{"role":"user","content":"check"},
		{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"lookup","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}
	]}`)
	observation.ResponseBody = []byte(`{"content":[{"type":"text","text":"Lookup completed successfully."}],"stop_reason":"end_turn"}`)
	return observation
}
