package agenttrajectory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Euphie/llm-proxy/internal/database"
	"github.com/Euphie/llm-proxy/internal/routing"
)

func TestStorePersistsEncryptedTrajectoryAndEnforcesLifecycle(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 7, 6, 0, 0, 0, time.UTC)
	store, cipher, closeStore := newTrajectoryStore(t, now)
	defer closeStore()

	payload := sealFixture(t, cipher, "first")
	sessionKey := bytes.Repeat([]byte{7}, 32)
	record, err := store.Create(ctx, CreateInput{
		ProfileID: 1, ProfileSlug: "test", Protocol: routing.OperationAnthropicMessages,
		Source: SourceSession, SessionKey: sessionKey,
		Strategy: "active", Route: "agent", TaskType: "tool_use", Difficulty: "medium",
		Risk: "normal", VisionMode: "none", ModelPath: []string{"model-a"},
		ToolCalls: 1, Turns: 1, Payload: payload,
		StartedAt: now.Add(-time.Minute), ExpiresAt: now.Add(7 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != StatusCollecting || record.ID <= 0 || bytes.Equal(record.Payload.Ciphertext, []byte("first")) {
		t.Fatalf("record=%+v", record)
	}
	found, ok, err := store.FindCollectingBySession(ctx, 1, sessionKey)
	if err != nil || !ok || found.ID != record.ID {
		t.Fatalf("found=%+v ok=%v err=%v", found, ok, err)
	}

	updatedPayload := sealFixture(t, cipher, "second")
	update := ContentUpdate{
		ModelPath: []string{"model-a"}, ToolCalls: 2, Turns: 2, ElapsedMS: 1500,
		Payload: updatedPayload,
	}
	if err := store.UpdateCollecting(ctx, record.ID, update); err != nil {
		t.Fatal(err)
	}
	completedAt := now.Add(time.Second)
	update.CompletedAt = &completedAt
	if err := store.Complete(ctx, record.ID, update); err != nil {
		t.Fatal(err)
	}
	if err := store.Complete(ctx, record.ID, update); !errors.Is(err, ErrConflict) {
		t.Fatalf("second Complete err=%v", err)
	}
	if err := store.UpdateCollecting(ctx, record.ID, update); !errors.Is(err, ErrConflict) {
		t.Fatalf("late UpdateCollecting err=%v", err)
	}
	if _, ok, err := store.FindCollectingBySession(ctx, 1, sessionKey); err != nil || ok {
		t.Fatalf("completed record remained active: ok=%v err=%v", ok, err)
	}
	if err := store.MarkQueued(ctx, record.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkEvaluating(ctx, record.ID); err != nil {
		t.Fatal(err)
	}
	evaluationResult := EvaluationResult{
		CandidateModel: "model-a", ReferenceModel: "model-b", ReviewerModel: "judge",
		Outcome: "candidate_win", Dimensions: map[string]string{
			"correctness": "candidate_win", "completeness": "tie",
			"instruction_following": "candidate_win", "format_tool_safety": "tie",
			"task_completion": "candidate_win",
		},
		CandidateCostMicroUSD: 20, ReferenceCostMicroUSD: 30, ReviewerCostMicroUSD: 40,
	}
	if err := store.MarkEvaluated(ctx, record.ID, 1234, evaluationResult); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusEvaluated || got.EvaluationCostMicroUSD != 1234 || !got.EvidenceRecorded ||
		got.EvaluationResult == nil || got.EvaluationResult.Outcome != "candidate_win" ||
		got.EvaluationResult.Dimensions["correctness"] != "candidate_win" ||
		got.ToolCalls != 2 || got.Turns != 2 || got.CompletedAt == nil {
		t.Fatalf("got=%+v", got)
	}
	opened, err := cipher.Open(got.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(opened.Events) != 1 || opened.Events[0].Text != "second" {
		t.Fatalf("opened=%+v", opened)
	}
}

func TestStoreListsFiltersPaginatesAndDeletes(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 7, 6, 0, 0, 0, time.UTC)
	store, cipher, closeStore := newTrajectoryStore(t, now)
	defer closeStore()

	for index := range 4 {
		record, err := store.Create(ctx, CreateInput{
			ProfileID: 1, ProfileSlug: "test", Protocol: routing.OperationOpenAIChatCompletions,
			Source: SourceRequestSnapshot, Strategy: "active", Route: "route-a",
			TaskType: "tool_use", Difficulty: "medium", Risk: []string{"normal", "high"}[index%2],
			VisionMode: "none", ModelPath: []string{fmt.Sprintf("model-%d", index%2)},
			Payload: sealFixture(t, cipher, fmt.Sprintf("item-%d", index)), ToolCalls: index,
			Turns: 1, StartedAt: now.Add(time.Duration(index) * time.Minute),
			ExpiresAt: now.Add(7 * 24 * time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		completedAt := now.Add(time.Duration(index) * time.Minute)
		if err := store.Complete(ctx, record.ID, ContentUpdate{
			ModelPath: record.ModelPath, ToolCalls: index, Turns: 1,
			Payload: record.Payload, CompletedAt: &completedAt,
		}); err != nil {
			t.Fatal(err)
		}
		if index%2 == 0 {
			if err := store.MarkSkipped(ctx, record.ID, "high_risk"); err != nil {
				t.Fatal(err)
			}
		}
	}

	result, err := store.List(ctx, ListFilter{
		ProfileID: 1, Status: StatusCompleted, Risk: "high", Model: "model-1", Limit: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 2 || len(result.Items) != 1 || result.Items[0].Risk != "high" || result.Items[0].Status != StatusCompleted {
		t.Fatalf("result=%+v", result)
	}
	if len(result.Items[0].Payload.Ciphertext) != 0 || len(result.Items[0].SessionKey) != 0 {
		t.Fatalf("list leaked encrypted/session data: %+v", result.Items[0])
	}
	secondPage, err := store.List(ctx, ListFilter{
		ProfileID: 1, Status: StatusCompleted, Risk: "high", Model: "model-1", Limit: 1, Offset: 1,
	})
	if err != nil || len(secondPage.Items) != 1 || secondPage.Items[0].ID == result.Items[0].ID {
		t.Fatalf("secondPage=%+v err=%v", secondPage, err)
	}
	if err := store.Delete(ctx, result.Items[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ctx, result.Items[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete err=%v", err)
	}
	if err := store.Delete(ctx, result.Items[0].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Delete err=%v", err)
	}
}

func TestStoreRetentionTimeoutAndStartupInterruption(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 7, 6, 0, 0, 0, time.UTC)
	store, cipher, closeStore := newTrajectoryStore(t, now)
	defer closeStore()

	create := func(source Source, key []byte, expiresAt time.Time) Record {
		record, err := store.Create(ctx, CreateInput{
			ProfileID: 1, ProfileSlug: "test", Protocol: routing.OperationOpenAIResponses,
			Source: source, SessionKey: key, Strategy: "active", Route: "route-a",
			TaskType: "tool_use", Difficulty: "medium", Risk: "normal", VisionMode: "none",
			ModelPath: []string{"model-a"}, Payload: sealFixture(t, cipher, "audit"),
			StartedAt: now.Add(-time.Hour), ExpiresAt: expiresAt,
		})
		if err != nil {
			t.Fatal(err)
		}
		return record
	}

	timedOut := create(SourceSession, bytes.Repeat([]byte{1}, 32), now.Add(24*time.Hour))
	if err := store.MarkTimedOut(ctx, timedOut.ID, "idle_timeout"); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Get(ctx, timedOut.ID); err != nil || got.Status != StatusTimedOut || got.ReasonCode != "idle_timeout" {
		t.Fatalf("timed out=%+v err=%v", got, err)
	}

	queued := create(SourceRequestSnapshot, nil, now.Add(24*time.Hour))
	completedAt := now
	if err := store.Complete(ctx, queued.ID, ContentUpdate{
		ModelPath: queued.ModelPath, Payload: queued.Payload, CompletedAt: &completedAt,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkQueued(ctx, queued.ID); err != nil {
		t.Fatal(err)
	}
	orphaned := create(SourceRequestSnapshot, nil, now.Add(24*time.Hour))
	if err := store.Complete(ctx, orphaned.ID, ContentUpdate{
		ModelPath: orphaned.ModelPath, Payload: orphaned.Payload, CompletedAt: &completedAt,
	}); err != nil {
		t.Fatal(err)
	}
	evaluating := create(SourceRequestSnapshot, nil, now.Add(24*time.Hour))
	if err := store.Complete(ctx, evaluating.ID, ContentUpdate{
		ModelPath: evaluating.ModelPath, Payload: evaluating.Payload, CompletedAt: &completedAt,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkQueued(ctx, evaluating.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkEvaluating(ctx, evaluating.ID); err != nil {
		t.Fatal(err)
	}
	count, err := store.MarkInterrupted(ctx)
	if err != nil || count != 3 {
		t.Fatalf("interrupted count=%d err=%v", count, err)
	}
	for _, id := range []int64{queued.ID, orphaned.ID, evaluating.ID} {
		got, err := store.Get(ctx, id)
		if err != nil || got.Status != StatusInterrupted || got.ReasonCode != "service_restarted" {
			t.Fatalf("id=%d got=%+v err=%v", id, got, err)
		}
	}

	expired := create(SourceRequestSnapshot, nil, now.Add(-time.Second))
	kept := create(SourceRequestSnapshot, nil, now.Add(time.Second))
	deleted, err := store.DeleteExpired(ctx)
	if err != nil || deleted != 1 {
		t.Fatalf("deleted=%d err=%v", deleted, err)
	}
	if _, err := store.Get(ctx, expired.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired Get err=%v", err)
	}
	if _, err := store.Get(ctx, kept.ID); err != nil {
		t.Fatalf("kept Get err=%v", err)
	}
}

func newTrajectoryStore(t *testing.T, now time.Time) (*Store, *Cipher, func()) {
	t.Helper()
	dataDir := t.TempDir()
	db, err := database.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO profiles (id, slug, display_name, enabled, config_json, created_at, updated_at)
		VALUES (1, 'test', 'Test', 1, '{}', ?, ?)
	`, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		db.Close()
		t.Fatal(err)
	}
	cipher, err := OpenCipher(dataDir)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	return NewStore(db, func() time.Time { return now }), cipher, func() { _ = db.Close() }
}

func sealFixture(t *testing.T, cipher *Cipher, text string) EncryptedPayload {
	t.Helper()
	payload, err := cipher.Seal(Plaintext{Events: []Event{{Role: "user", Kind: EventUserMessage, Text: text}}})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
