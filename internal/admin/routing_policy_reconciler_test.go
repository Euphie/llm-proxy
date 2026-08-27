package admin

import (
	"context"
	"database/sql"
	"net/http"
	"testing"
	"time"

	"github.com/Euphie/llm-proxy/internal/database"
	"github.com/Euphie/llm-proxy/internal/gateway"
	"github.com/Euphie/llm-proxy/internal/profile"
	"github.com/Euphie/llm-proxy/internal/runtimeconfig"
	"github.com/Euphie/llm-proxy/internal/strategycompiler"
)

type reconcileRuntimeFake struct {
	aggregate runtimeconfig.Aggregate
	applied   []profile.RoutingPolicyConfig
	reasons   []string
	automatic []int64
}

func (runtime *reconcileRuntimeFake) Load(
	_ context.Context,
	_ int64,
) (runtimeconfig.Aggregate, error) {
	return runtime.aggregate, nil
}

func (runtime *reconcileRuntimeFake) ApplyAutomatic(
	_ context.Context,
	_ int64,
	expectedRevision int64,
	policy profile.RoutingPolicyConfig,
	reason string,
) error {
	if runtime.aggregate.State.Revision != expectedRevision {
		return runtimeconfig.ErrRevisionConflict
	}
	runtime.applied = append(runtime.applied, policy)
	runtime.reasons = append(runtime.reasons, reason)
	runtime.aggregate.State.Revision++
	runtime.aggregate.Active.Policy = policy
	return nil
}

func (runtime *reconcileRuntimeFake) AutomaticProfileIDs(context.Context) ([]int64, error) {
	return append([]int64(nil), runtime.automatic...), nil
}

type calibrationSequence struct {
	results []strategycompiler.CalibrationResult
	calls   int
}

func (sequence *calibrationSequence) Calibrate(
	_ context.Context,
	_ profile.Record,
	_ profile.RoutingPolicyConfig,
) (strategycompiler.CalibrationResult, error) {
	index := sequence.calls
	if index >= len(sequence.results) {
		index = len(sequence.results) - 1
	}
	sequence.calls++
	return sequence.results[index], nil
}

func TestRoutingPolicyReconcilerRequiresTwoMatchingEvidenceWindows(t *testing.T) {
	now := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	reconciler, runtime := newReconcilerFixture(t, &now, []strategycompiler.CalibrationResult{
		calibrationChange("stable-digest", false),
		calibrationChange("stable-digest", false),
	})

	if err := reconciler.MarkDirty(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.ReconcileNow(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	status, err := reconciler.Status(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(runtime.applied) != 0 || status.PendingDigest != "stable-digest" ||
		status.ConfirmationCount != 1 || status.Dirty {
		t.Fatalf("first window: applied=%d status=%+v", len(runtime.applied), status)
	}

	now = now.Add(time.Hour)
	if err := reconciler.MarkDirty(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.ReconcileNow(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	status, err = reconciler.Status(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(runtime.applied) != 1 || status.LastOutcome != "applied" ||
		status.ConfirmationCount != 0 || status.Dirty || status.LastAppliedAt == nil {
		t.Fatalf("second window: applied=%d status=%+v", len(runtime.applied), status)
	}
}

func TestRoutingPolicyReconcilerCriticalRegressionBypassesConfirmationAndCooldown(t *testing.T) {
	now := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	reconciler, runtime := newReconcilerFixture(t, &now, []strategycompiler.CalibrationResult{
		calibrationChange("first", false), calibrationChange("first", false),
		calibrationChange("critical", true),
	})

	for range 2 {
		if err := reconciler.MarkDirty(context.Background(), 1); err != nil {
			t.Fatal(err)
		}
		if err := reconciler.ReconcileNow(context.Background(), 1); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Minute)
	}
	if len(runtime.applied) != 1 {
		t.Fatalf("initial applied=%d", len(runtime.applied))
	}
	if err := reconciler.MarkDirty(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.ReconcileNow(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if len(runtime.applied) != 2 {
		t.Fatalf("critical applied=%d want 2", len(runtime.applied))
	}
}

func TestRoutingPolicyReconcilerDefersOrdinaryChangeDuringCooldown(t *testing.T) {
	now := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	reconciler, runtime := newReconcilerFixture(t, &now, []strategycompiler.CalibrationResult{
		calibrationChange("first", false), calibrationChange("first", false),
		calibrationChange("second", false),
	})
	for range 2 {
		if err := reconciler.MarkDirty(context.Background(), 1); err != nil {
			t.Fatal(err)
		}
		if err := reconciler.ReconcileNow(context.Background(), 1); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Minute)
	}
	if err := reconciler.MarkDirty(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if err := reconciler.ReconcileNow(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	status, err := reconciler.Status(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(runtime.applied) != 1 || !status.Dirty || status.NextAttemptAt == nil ||
		!status.NextAttemptAt.Equal(now.Add(6*time.Hour-time.Minute)) {
		t.Fatalf("applied=%d status=%+v now=%s", len(runtime.applied), status, now)
	}
}

func TestRoutingPolicyReconcilerPersistsDirtyStateAcrossInstances(t *testing.T) {
	now := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	insertReconcileProfile(t, db)
	runtime := reconcileRuntimeFixture(now)
	first := NewRoutingPolicyReconciler(db, runtime, &calibrationSequence{results: []strategycompiler.CalibrationResult{
		calibrationChange("persisted", false),
	}}, RoutingPolicyReconcilerOptions{Now: func() time.Time { return now }})
	if err := first.MarkDirty(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	second := NewRoutingPolicyReconciler(db, runtime, &calibrationSequence{results: []strategycompiler.CalibrationResult{
		calibrationChange("persisted", false),
	}}, RoutingPolicyReconcilerOptions{Now: func() time.Time { return now }})
	status, err := second.Status(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Dirty || status.NextAttemptAt == nil || !status.NextAttemptAt.Equal(now.Add(10*time.Minute)) {
		t.Fatalf("status=%+v", status)
	}
}

func TestRoutingPolicyReconcilerPeriodicAuditMarksAutomaticProfilesDirty(t *testing.T) {
	now := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	reconciler, runtime := newReconcilerFixture(t, &now, []strategycompiler.CalibrationResult{
		calibrationChange("audit", false),
	})
	runtime.automatic = []int64{1}
	if err := reconciler.auditAutomaticProfiles(context.Background()); err != nil {
		t.Fatal(err)
	}
	status, err := reconciler.Status(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Dirty || status.NextAttemptAt == nil {
		t.Fatalf("status=%+v", status)
	}
}

func TestRoutingPolicyServiceApplyAutomaticWritesSystemAuditIdentity(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	policy := insertAPIV2PolicyProfile(t, db, 151)
	registry := gateway.NewRegistry()
	coordinator := gateway.NewRuntimeCoordinator(registry, func(aggregate runtimeconfig.Aggregate) (http.Handler, error) {
		if _, err := profile.ResolvePolicyRuntime(aggregate.Profile, aggregate.Models, &aggregate.Active.Policy); err != nil {
			return nil, err
		}
		return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil
	})
	service := NewRoutingPolicyService(runtimeconfig.NewStore(db, nil), coordinator)
	policy.Alias = "自动校准"
	if err := service.ApplyAutomatic(context.Background(), 151, 1, policy, "可靠证据发生变化"); err != nil {
		t.Fatal(err)
	}
	var actor, reason, kind string
	if err := db.QueryRow(`
		SELECT created_by, change_reason, change_kind FROM routing_policy_versions
		WHERE profile_id = 151 AND policy_sequence = 2
	`).Scan(&actor, &reason, &kind); err != nil {
		t.Fatal(err)
	}
	if actor != policyReconcilerActor || reason != "可靠证据发生变化" || kind != "apply" {
		t.Fatalf("actor=%q reason=%q kind=%q", actor, reason, kind)
	}
}

func TestRoutingPolicyOverviewIncludesAutomaticReconciliationStatus(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	insertAPIV2PolicyProfile(t, db, 152)
	store := runtimeconfig.NewStore(db, nil)
	service := NewRoutingPolicyService(store, nil)
	reconciler := NewRoutingPolicyReconciler(
		db, service, &calibrationSequence{}, RoutingPolicyReconcilerOptions{},
	)
	service.EnableAutomaticReconciliation(reconciler)
	if err := reconciler.MarkDirty(context.Background(), 152); err != nil {
		t.Fatal(err)
	}

	overview, err := service.Overview(context.Background(), 152)
	if err != nil {
		t.Fatal(err)
	}
	if !overview.AutomaticUpdate.Dirty || overview.AutomaticUpdate.ProfileID != 152 ||
		overview.AutomaticUpdate.NextAttemptAt == nil {
		t.Fatalf("automatic update status=%+v", overview.AutomaticUpdate)
	}
}

func newReconcilerFixture(
	t *testing.T,
	now *time.Time,
	results []strategycompiler.CalibrationResult,
) (*RoutingPolicyReconciler, *reconcileRuntimeFake) {
	t.Helper()
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	insertReconcileProfile(t, db)
	runtime := reconcileRuntimeFixture(*now)
	reconciler := NewRoutingPolicyReconciler(
		db, runtime, &calibrationSequence{results: results},
		RoutingPolicyReconcilerOptions{Now: func() time.Time { return *now }},
	)
	return reconciler, runtime
}

func reconcileRuntimeFixture(now time.Time) *reconcileRuntimeFake {
	policy := calibrationChange("active", false).Policy
	return &reconcileRuntimeFake{aggregate: runtimeconfig.Aggregate{
		Profile: profile.Record{ID: 1, Slug: "dynamic", DisplayName: "Dynamic", Enabled: true,
			Config: profile.Config{Version: 2}},
		Active: &runtimeconfig.PolicyVersion{ID: 1, ProfileID: 1, Sequence: 1, Policy: policy, CreatedAt: now.Add(-24 * time.Hour)},
		State:  runtimeconfig.State{ProfileID: 1, Revision: 1, ActivePolicyVersionID: 1, ModelCatalogRevision: 1},
	}}
}

func calibrationChange(digest string, critical bool) strategycompiler.CalibrationResult {
	return strategycompiler.CalibrationResult{
		Policy: profile.RoutingPolicyConfig{
			RoutingStrategyConfig: profile.RoutingStrategyConfig{Name: "20260817-001"},
			DynamicOptimization: profile.DynamicOptimizationConfig{
				Enabled: true, AutoUpdatePolicy: true,
			},
		},
		Changed: true, Critical: critical, Digest: digest, ReliableCandidates: 1,
	}
}

func insertReconcileProfile(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`
		INSERT INTO profiles (id, slug, display_name, enabled, config_json, created_at, updated_at)
		VALUES (1, 'dynamic', 'Dynamic', 1, '{"version":2}',
		  '2026-08-17T00:00:00Z', '2026-08-17T00:00:00Z')
	`); err != nil {
		t.Fatal(err)
	}
}

var _ policyReconcileRuntime = (*reconcileRuntimeFake)(nil)
var _ policyCalibrator = (*calibrationSequence)(nil)
