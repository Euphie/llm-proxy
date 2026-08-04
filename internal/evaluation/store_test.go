package evaluation

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Euphie/llm-proxy/internal/database"
)

func TestBudgetReservationCommitsActualSpendAndReleasesUnusedAmount(t *testing.T) {
	db := openEvaluationDB(t)
	profileID := insertEvaluationProfile(t, db)
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	store := NewStore(db, func() time.Time { return now })

	first, err := store.ReserveBudget(context.Background(), profileID, 100, 70)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReserveBudget(context.Background(), profileID, 100, 31); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("second reserve error=%v, want ErrBudgetExceeded", err)
	}
	if err := first.Commit(context.Background(), 45); err != nil {
		t.Fatal(err)
	}

	second, err := store.ReserveBudget(context.Background(), profileID, 100, 55)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Release(context.Background()); err != nil {
		t.Fatal(err)
	}

	snapshot, err := store.Budget(context.Background(), profileID, now)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ReservedMicroUSD != 0 || snapshot.SpentMicroUSD != 45 {
		t.Fatalf("budget snapshot=%+v", snapshot)
	}
}

func TestBudgetReservationIsAtomicAcrossConcurrentWorkers(t *testing.T) {
	db := openEvaluationDB(t)
	profileID := insertEvaluationProfile(t, db)
	store := NewStore(db, func() time.Time {
		return time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	})

	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := 0
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reservation, err := store.ReserveBudget(context.Background(), profileID, 50, 10)
			switch {
			case err == nil:
				mu.Lock()
				accepted++
				mu.Unlock()
				_ = reservation.Commit(context.Background(), 10)
			case errors.Is(err, ErrBudgetExceeded):
			default:
				t.Errorf("ReserveBudget() error=%v", err)
			}
		}()
	}
	wg.Wait()

	if accepted != 5 {
		t.Fatalf("accepted=%d, want 5", accepted)
	}
	snapshot, err := store.Budget(context.Background(), profileID, time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ReservedMicroUSD != 0 || snapshot.SpentMicroUSD != 50 {
		t.Fatalf("budget snapshot=%+v", snapshot)
	}
}

func TestResetReservationsRecoversInMemoryJobsLostOnRestart(t *testing.T) {
	db := openEvaluationDB(t)
	profileID := insertEvaluationProfile(t, db)
	store := NewStore(db, func() time.Time {
		return time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	})

	if _, err := store.ReserveBudget(context.Background(), profileID, 100, 80); err != nil {
		t.Fatal(err)
	}
	if err := store.ResetReservations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReserveBudget(context.Background(), profileID, 100, 100); err != nil {
		t.Fatalf("reserve after reset: %v", err)
	}
}

func TestRecordEvidenceAggregatesOnlyMetricsByDay(t *testing.T) {
	db := openEvaluationDB(t)
	profileID := insertEvaluationProfile(t, db)
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	store := NewStore(db, func() time.Time { return now })
	base := Evidence{
		ProfileID: profileID, Strategy: "20260802-001", Route: "balanced",
		TaskType: "simple", Difficulty: "easy", Risk: "normal", VisionMode: "none",
		CandidateModel: "fast", ReferenceModel: "strong",
		ReviewerModel: "judge", CandidateCostMicroUSD: 10,
		ReferenceCostMicroUSD: 40, ReviewerCostMicroUSD: 5,
		CandidateLatencyMS: 20, ReferenceLatencyMS: 30,
		Dimensions: dimensionOutcomes(OutcomeTie),
	}
	first := base
	first.Outcome = OutcomeCandidateWin
	first.SelfEscalationEligible = true
	if err := store.RecordEvidence(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := base
	second.Outcome = OutcomeTie
	second.SevereError = true
	second.DeterministicFailure = true
	second.SelfEscalationEligible = true
	second.SelfEscalationRequested = true
	second.SelfEscalationSupported = true
	if err := store.RecordEvidence(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	third := base
	third.Outcome = OutcomeTie
	third.SelfEscalationEligible = true
	third.SelfEscalationRequested = true
	third.SelfEscalationUnnecessary = true
	if err := store.RecordEvidence(context.Background(), third); err != nil {
		t.Fatal(err)
	}
	fourth := base
	fourth.Outcome = OutcomeReferenceWin
	fourth.SelfEscalationEligible = true
	fourth.SelfEscalationMissed = true
	if err := store.RecordEvidence(context.Background(), fourth); err != nil {
		t.Fatal(err)
	}

	rows, err := store.ListEvidence(context.Background(), profileID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("evidence rows=%d, want 1", len(rows))
	}
	got := rows[0]
	if got.Day != "2026-08-02" || got.Samples != 4 || got.CandidateWins != 1 ||
		got.Ties != 2 || got.ReferenceWins != 1 || got.SevereErrors != 1 ||
		got.DeterministicFailures != 1 || got.CandidateCostMicroUSD != 40 ||
		got.ReferenceCostMicroUSD != 160 || got.ReviewerCostMicroUSD != 20 ||
		got.CandidateLatencyMS != 80 || got.ReferenceLatencyMS != 120 ||
		got.SelfEscalationEligibleSamples != 4 || got.SelfEscalations != 2 ||
		got.SupportedSelfEscalations != 1 || got.UnnecessarySelfEscalations != 1 ||
		got.MissedSelfEscalations != 1 {
		t.Fatalf("evidence=%+v", got)
	}
}

func TestRecordEvidenceKeepsClassificationBucketsSeparate(t *testing.T) {
	db := openEvaluationDB(t)
	profileID := insertEvaluationProfile(t, db)
	store := NewStore(db, func() time.Time {
		return time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	})
	base := Evidence{
		ProfileID: profileID, Strategy: "20260802-001", Route: "balanced",
		TaskType: "code", Difficulty: "easy", Risk: "normal", VisionMode: "none",
		CandidateModel: "fast", ReferenceModel: "strong", ReviewerModel: "judge",
		Outcome: OutcomeCandidateWin, Dimensions: dimensionOutcomes(OutcomeCandidateWin),
	}
	for _, mutate := range []func(*Evidence){
		func(*Evidence) {},
		func(e *Evidence) { e.Difficulty = "hard" },
		func(e *Evidence) { e.Risk = "high" },
		func(e *Evidence) { e.VisionMode = "composite" },
	} {
		evidence := base
		mutate(&evidence)
		if err := store.RecordEvidence(context.Background(), evidence); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := store.ListEvidence(context.Background(), profileID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("evidence buckets=%d, want 4: %+v", len(rows), rows)
	}
	for _, row := range rows {
		if len(row.Dimensions) != len(ReviewDimensions) {
			t.Fatalf("dimension aggregates=%+v", row.Dimensions)
		}
	}
}

func TestEvidenceAggregateAndDimensionsUseOneReadSnapshot(t *testing.T) {
	db := openEvaluationDB(t)
	profileID := insertEvaluationProfile(t, db)
	store := NewStore(db, func() time.Time {
		return time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	})
	evidence := Evidence{
		ProfileID: profileID, Strategy: "20260802-001", Route: "balanced",
		TaskType: "code", Difficulty: "easy", Risk: "normal", VisionMode: "none",
		CandidateModel: "fast", ReferenceModel: "strong", ReviewerModel: "judge",
		Outcome: OutcomeCandidateWin, Dimensions: dimensionOutcomes(OutcomeCandidateWin),
	}
	if err := store.RecordEvidence(context.Background(), evidence); err != nil {
		t.Fatal(err)
	}

	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	rows, err := listEvidenceAggregates(context.Background(), tx, profileID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordEvidence(context.Background(), evidence); err != nil {
		t.Fatal(err)
	}
	if err := attachDimensionEvidence(context.Background(), tx, profileID, rows); err != nil {
		t.Fatal(err)
	}
	if rows[0].Samples != 1 || rows[0].Dimensions[DimensionCorrectness].Samples != 1 {
		t.Fatalf("mixed read snapshot: overall=%d dimension=%d", rows[0].Samples,
			rows[0].Dimensions[DimensionCorrectness].Samples)
	}
}

func dimensionOutcomes(outcome Outcome) map[Dimension]Outcome {
	result := make(map[Dimension]Outcome, len(ReviewDimensions))
	for _, dimension := range ReviewDimensions {
		result[dimension] = outcome
	}
	return result
}

func openEvaluationDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func insertEvaluationProfile(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	result, err := db.Exec(`
		INSERT INTO profiles (slug, display_name, enabled, config_json, created_at, updated_at)
		VALUES ('evaluation', 'Evaluation', 1, '{}', 'now', 'now')
	`)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}
