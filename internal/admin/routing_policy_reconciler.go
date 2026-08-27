package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Euphie/llm-proxy/internal/profile"
	"github.com/Euphie/llm-proxy/internal/runtimeconfig"
	"github.com/Euphie/llm-proxy/internal/strategycompiler"
)

const policyReconcilerActor = "system:policy-reconciler"

type policyReconcileRuntime interface {
	Load(context.Context, int64) (runtimeconfig.Aggregate, error)
	ApplyAutomatic(context.Context, int64, int64, profile.RoutingPolicyConfig, string) error
	AutomaticProfileIDs(context.Context) ([]int64, error)
}

type policyCalibrator interface {
	Calibrate(
		context.Context,
		profile.Record,
		profile.RoutingPolicyConfig,
	) (strategycompiler.CalibrationResult, error)
}

type RoutingPolicyReconcilerOptions struct {
	Now                   func() time.Time
	Debounce              time.Duration
	Cooldown              time.Duration
	RequiredConfirmations int
	ScanInterval          time.Duration
	AuditInterval         time.Duration
}

type RoutingPolicyReconcileStatus struct {
	ProfileID         int64      `json:"profile_id"`
	Dirty             bool       `json:"dirty"`
	DirtyAt           *time.Time `json:"dirty_at,omitempty"`
	NextAttemptAt     *time.Time `json:"next_attempt_at,omitempty"`
	PendingDigest     string     `json:"pending_digest"`
	ConfirmationCount int        `json:"confirmation_count"`
	LastAttemptAt     *time.Time `json:"last_attempt_at,omitempty"`
	LastAppliedAt     *time.Time `json:"last_applied_at,omitempty"`
	LastOutcome       string     `json:"last_outcome"`
	LastReason        string     `json:"last_reason"`
}

type RoutingPolicyReconciler struct {
	db                    *sql.DB
	runtime               policyReconcileRuntime
	calibrator            policyCalibrator
	now                   func() time.Time
	debounce              time.Duration
	cooldown              time.Duration
	requiredConfirmations int
	scanInterval          time.Duration
	auditInterval         time.Duration
	wake                  chan struct{}

	startOnce sync.Once
	closeOnce sync.Once
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

func NewRoutingPolicyReconciler(
	db *sql.DB,
	runtime policyReconcileRuntime,
	calibrator policyCalibrator,
	options RoutingPolicyReconcilerOptions,
) *RoutingPolicyReconciler {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	debounce := options.Debounce
	if debounce <= 0 {
		debounce = 10 * time.Minute
	}
	cooldown := options.Cooldown
	if cooldown <= 0 {
		cooldown = 6 * time.Hour
	}
	confirmations := options.RequiredConfirmations
	if confirmations <= 0 {
		confirmations = 2
	}
	scanInterval := options.ScanInterval
	if scanInterval <= 0 {
		scanInterval = time.Minute
	}
	auditInterval := options.AuditInterval
	if auditInterval <= 0 {
		auditInterval = 30 * time.Minute
	}
	return &RoutingPolicyReconciler{
		db: db, runtime: runtime, calibrator: calibrator, now: now,
		debounce: debounce, cooldown: cooldown,
		requiredConfirmations: confirmations, scanInterval: scanInterval,
		auditInterval: auditInterval,
		wake:          make(chan struct{}, 1),
	}
}

func (reconciler *RoutingPolicyReconciler) MarkDirty(ctx context.Context, profileID int64) error {
	if reconciler == nil || reconciler.db == nil || profileID <= 0 {
		return errors.New("invalid routing Policy reconciliation request")
	}
	now := reconciler.now().UTC()
	next := now.Add(reconciler.debounce)
	_, err := reconciler.db.ExecContext(ctx, `
		INSERT INTO routing_policy_reconcile_state (
			profile_id, dirty_at, next_attempt_at, updated_at
		) VALUES (?, ?, ?, ?)
		ON CONFLICT(profile_id) DO UPDATE SET
			dirty_at = CASE
				WHEN routing_policy_reconcile_state.dirty_at IS NULL THEN excluded.dirty_at
				ELSE routing_policy_reconcile_state.dirty_at
			END,
			next_attempt_at = CASE
				WHEN routing_policy_reconcile_state.dirty_at IS NULL THEN excluded.next_attempt_at
				ELSE routing_policy_reconcile_state.next_attempt_at
			END,
			updated_at = excluded.updated_at
	`, profileID, formatReconcileTime(now), formatReconcileTime(next), formatReconcileTime(now))
	if err != nil {
		return fmt.Errorf("mark routing Policy reconciliation dirty: %w", err)
	}
	select {
	case reconciler.wake <- struct{}{}:
	default:
	}
	return nil
}

func (reconciler *RoutingPolicyReconciler) Status(
	ctx context.Context,
	profileID int64,
) (RoutingPolicyReconcileStatus, error) {
	status := RoutingPolicyReconcileStatus{ProfileID: profileID}
	var dirtyAt, nextAttemptAt, lastAttemptAt, lastAppliedAt sql.NullString
	err := reconciler.db.QueryRowContext(ctx, `
		SELECT dirty_at, next_attempt_at, pending_digest, confirmation_count,
		       last_attempt_at, last_applied_at, last_outcome, last_reason
		FROM routing_policy_reconcile_state WHERE profile_id = ?
	`, profileID).Scan(
		&dirtyAt, &nextAttemptAt, &status.PendingDigest, &status.ConfirmationCount,
		&lastAttemptAt, &lastAppliedAt, &status.LastOutcome, &status.LastReason,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return status, nil
	}
	if err != nil {
		return RoutingPolicyReconcileStatus{}, fmt.Errorf("read routing Policy reconciliation status: %w", err)
	}
	if status.DirtyAt, err = parseOptionalReconcileTime(dirtyAt); err != nil {
		return RoutingPolicyReconcileStatus{}, err
	}
	status.Dirty = status.DirtyAt != nil
	if status.NextAttemptAt, err = parseOptionalReconcileTime(nextAttemptAt); err != nil {
		return RoutingPolicyReconcileStatus{}, err
	}
	if status.LastAttemptAt, err = parseOptionalReconcileTime(lastAttemptAt); err != nil {
		return RoutingPolicyReconcileStatus{}, err
	}
	if status.LastAppliedAt, err = parseOptionalReconcileTime(lastAppliedAt); err != nil {
		return RoutingPolicyReconcileStatus{}, err
	}
	return status, nil
}

func (reconciler *RoutingPolicyReconciler) ReconcileNow(ctx context.Context, profileID int64) error {
	status, err := reconciler.Status(ctx, profileID)
	if err != nil {
		return err
	}
	aggregate, err := reconciler.runtime.Load(ctx, profileID)
	if err != nil {
		return reconciler.retry(ctx, status, "load_failed", err.Error())
	}
	if aggregate.Active == nil {
		return reconciler.complete(ctx, status, "skipped", "当前没有线上策略。", false)
	}
	optimization := aggregate.Active.Policy.DynamicOptimization
	if !optimization.Enabled || !optimization.AutoUpdatePolicy {
		return reconciler.complete(ctx, status, "disabled", "自动更新策略未启用。", false)
	}
	result, err := reconciler.calibrator.Calibrate(ctx, aggregate.Profile, aggregate.Active.Policy)
	if err != nil {
		return reconciler.retry(ctx, status, "calibration_failed", err.Error())
	}
	if !result.Changed {
		return reconciler.complete(ctx, status, "no_change", "没有达到发布阈值的评分变化。", false)
	}

	now := reconciler.now().UTC()
	if !result.Critical {
		cooldownStart := aggregate.Active.CreatedAt
		if status.LastAppliedAt != nil && status.LastAppliedAt.After(cooldownStart) {
			cooldownStart = *status.LastAppliedAt
		}
		if cooldownUntil := cooldownStart.Add(reconciler.cooldown); cooldownUntil.After(now) {
			return reconciler.deferUntil(ctx, status, cooldownUntil, "最近策略刚生效，等待冷却期结束。")
		}
		confirmations := 1
		if status.PendingDigest == result.Digest {
			confirmations = status.ConfirmationCount + 1
		}
		if confirmations < reconciler.requiredConfirmations {
			return reconciler.waitForConfirmation(ctx, status, result.Digest, confirmations)
		}
	}

	reason := fmt.Sprintf(
		"动态策略自动校准：%d 个可靠候选产生%s变化。",
		result.ReliableCandidates,
		map[bool]string{true: "安全回归", false: "连续确认"}[result.Critical],
	)
	if err := reconciler.runtime.ApplyAutomatic(
		ctx, profileID, aggregate.State.Revision, result.Policy, reason,
	); err != nil {
		return reconciler.retry(ctx, status, "apply_failed", err.Error())
	}
	return reconciler.complete(ctx, status, "applied", reason, true)
}

func (reconciler *RoutingPolicyReconciler) Start() {
	if reconciler == nil {
		return
	}
	reconciler.startOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		reconciler.cancel = cancel
		reconciler.wg.Add(1)
		go reconciler.run(ctx)
	})
}

func (reconciler *RoutingPolicyReconciler) Close() {
	if reconciler == nil {
		return
	}
	reconciler.closeOnce.Do(func() {
		if reconciler.cancel != nil {
			reconciler.cancel()
		}
		reconciler.wg.Wait()
	})
}

func (reconciler *RoutingPolicyReconciler) run(ctx context.Context) {
	defer reconciler.wg.Done()
	scanTicker := time.NewTicker(reconciler.scanInterval)
	defer scanTicker.Stop()
	auditTicker := time.NewTicker(reconciler.auditInterval)
	defer auditTicker.Stop()
	if err := reconciler.auditAutomaticProfiles(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("routing.policy_reconciler.audit_failed", "error", err)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-scanTicker.C:
		case <-auditTicker.C:
			if err := reconciler.auditAutomaticProfiles(ctx); err != nil && !errors.Is(err, context.Canceled) {
				slog.Warn("routing.policy_reconciler.audit_failed", "error", err)
			}
		case <-reconciler.wake:
		}
		reconciler.reconcileDue(ctx)
	}
}

func (reconciler *RoutingPolicyReconciler) auditAutomaticProfiles(ctx context.Context) error {
	profileIDs, err := reconciler.runtime.AutomaticProfileIDs(ctx)
	if err != nil {
		return err
	}
	for _, profileID := range profileIDs {
		if err := reconciler.MarkDirty(ctx, profileID); err != nil {
			return err
		}
	}
	return nil
}

func (reconciler *RoutingPolicyReconciler) reconcileDue(ctx context.Context) {
	rows, err := reconciler.db.QueryContext(ctx, `
		SELECT profile_id FROM routing_policy_reconcile_state
		WHERE dirty_at IS NOT NULL AND next_attempt_at IS NOT NULL AND next_attempt_at <= ?
		ORDER BY next_attempt_at, profile_id LIMIT 100
	`, formatReconcileTime(reconciler.now().UTC()))
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			slog.Warn("routing.policy_reconciler.scan_failed", "error", err)
		}
		return
	}
	var profileIDs []int64
	for rows.Next() {
		var profileID int64
		if err := rows.Scan(&profileID); err != nil {
			_ = rows.Close()
			slog.Warn("routing.policy_reconciler.scan_failed", "error", err)
			return
		}
		profileIDs = append(profileIDs, profileID)
	}
	_ = rows.Close()
	for _, profileID := range profileIDs {
		if err := reconciler.ReconcileNow(ctx, profileID); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("routing.policy_reconciler.failed", "profile_id", profileID, "error", err)
		}
	}
}

func (reconciler *RoutingPolicyReconciler) waitForConfirmation(
	ctx context.Context,
	status RoutingPolicyReconcileStatus,
	digest string,
	confirmations int,
) error {
	now := reconciler.now().UTC()
	_, err := reconciler.db.ExecContext(ctx, `
		UPDATE routing_policy_reconcile_state
		SET dirty_at = NULL, next_attempt_at = NULL, pending_digest = ?,
		    confirmation_count = ?, last_attempt_at = ?, last_outcome = 'waiting_confirmation',
		    last_reason = ?, updated_at = ? WHERE profile_id = ?
	`, digest, confirmations, formatReconcileTime(now),
		fmt.Sprintf("等待第 %d/%d 次一致证据窗口。", confirmations+1, reconciler.requiredConfirmations),
		formatReconcileTime(now), status.ProfileID)
	if err != nil {
		return fmt.Errorf("save routing Policy confirmation: %w", err)
	}
	return nil
}

func (reconciler *RoutingPolicyReconciler) deferUntil(
	ctx context.Context,
	status RoutingPolicyReconcileStatus,
	next time.Time,
	reason string,
) error {
	now := reconciler.now().UTC()
	_, err := reconciler.db.ExecContext(ctx, `
		UPDATE routing_policy_reconcile_state
		SET next_attempt_at = ?, last_attempt_at = ?, last_outcome = 'cooldown',
		    last_reason = ?, updated_at = ? WHERE profile_id = ?
	`, formatReconcileTime(next), formatReconcileTime(now), reason,
		formatReconcileTime(now), status.ProfileID)
	if err != nil {
		return fmt.Errorf("defer routing Policy reconciliation: %w", err)
	}
	return nil
}

func (reconciler *RoutingPolicyReconciler) complete(
	ctx context.Context,
	status RoutingPolicyReconcileStatus,
	outcome string,
	reason string,
	applied bool,
) error {
	now := reconciler.now().UTC()
	lastApplied := any(nil)
	if applied {
		lastApplied = formatReconcileTime(now)
	}
	_, err := reconciler.db.ExecContext(ctx, `
		UPDATE routing_policy_reconcile_state
		SET dirty_at = NULL, next_attempt_at = NULL, pending_digest = '',
		    confirmation_count = 0, last_attempt_at = ?,
		    last_applied_at = COALESCE(?, last_applied_at),
		    last_outcome = ?, last_reason = ?, updated_at = ? WHERE profile_id = ?
	`, formatReconcileTime(now), lastApplied, outcome, reason,
		formatReconcileTime(now), status.ProfileID)
	if err != nil {
		return fmt.Errorf("complete routing Policy reconciliation: %w", err)
	}
	return nil
}

func (reconciler *RoutingPolicyReconciler) retry(
	ctx context.Context,
	status RoutingPolicyReconcileStatus,
	outcome string,
	reason string,
) error {
	now := reconciler.now().UTC()
	_, saveErr := reconciler.db.ExecContext(ctx, `
		UPDATE routing_policy_reconcile_state
		SET next_attempt_at = ?, last_attempt_at = ?, last_outcome = ?,
		    last_reason = ?, updated_at = ? WHERE profile_id = ?
	`, formatReconcileTime(now.Add(reconciler.debounce)), formatReconcileTime(now),
		outcome, reason, formatReconcileTime(now), status.ProfileID)
	if saveErr != nil {
		return fmt.Errorf("save routing Policy reconciliation retry: %w", saveErr)
	}
	return errors.New(reason)
}

func parseOptionalReconcileTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid || value.String == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value.String)
	if err != nil {
		return nil, fmt.Errorf("parse routing Policy reconciliation time: %w", err)
	}
	return &parsed, nil
}

func formatReconcileTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}
