package admin

import (
	"context"
	"errors"
	"fmt"

	"github.com/Euphie/llm-proxy/internal/gateway"
	"github.com/Euphie/llm-proxy/internal/profile"
	"github.com/Euphie/llm-proxy/internal/runtimeconfig"
	"github.com/Euphie/llm-proxy/internal/strategycompiler"
)

type RoutingPolicyOverview struct {
	RuntimeState    runtimeconfig.State          `json:"runtime_state"`
	Active          *runtimeconfig.PolicyVersion `json:"active,omitempty"`
	History         []RoutingPolicyHistoryEntry  `json:"history"`
	AutomaticUpdate RoutingPolicyReconcileStatus `json:"automatic_update"`
}

type RoutingPolicyHistoryEntry struct {
	runtimeconfig.PolicyVersion
	RollbackCompatible bool   `json:"rollback_compatible"`
	CompatibilityError string `json:"compatibility_error,omitempty"`
}

func (service *RoutingPolicyService) Apply(
	ctx context.Context,
	profileID int64,
	expectedRevision int64,
	policy profile.RoutingPolicyConfig,
	reason string,
) (RoutingPolicyOverview, error) {
	return service.apply(ctx, profileID, expectedRevision, policy, reason, "admin")
}

func (service *RoutingPolicyService) ApplyAutomatic(
	ctx context.Context,
	profileID int64,
	expectedRevision int64,
	policy profile.RoutingPolicyConfig,
	reason string,
) error {
	_, err := service.apply(
		ctx, profileID, expectedRevision, policy, reason, policyReconcilerActor,
	)
	return err
}

func (service *RoutingPolicyService) Load(
	ctx context.Context,
	profileID int64,
) (runtimeconfig.Aggregate, error) {
	return service.store.Load(ctx, profileID)
}

func (service *RoutingPolicyService) AutomaticProfileIDs(ctx context.Context) ([]int64, error) {
	return service.store.AutomaticProfileIDs(ctx)
}

func (service *RoutingPolicyService) apply(
	ctx context.Context,
	profileID int64,
	expectedRevision int64,
	policy profile.RoutingPolicyConfig,
	reason string,
	actor string,
) (RoutingPolicyOverview, error) {
	if service.coordinator == nil {
		return RoutingPolicyOverview{}, errors.New("routing Policy runtime coordinator is unavailable")
	}
	aggregate, err := service.store.Load(ctx, profileID)
	if err != nil {
		return RoutingPolicyOverview{}, err
	}
	if aggregate.State.Revision != expectedRevision {
		return RoutingPolicyOverview{}, runtimeconfig.ErrRevisionConflict
	}
	resetOrdinaryLocks, err := ordinarySessionLockThresholdChanged(aggregate, policy)
	if err != nil {
		return RoutingPolicyOverview{}, err
	}
	prepared, err := service.store.PreparePolicy(ctx, runtimeconfig.ApplyPolicyInput{
		ProfileID: profileID, ExpectedRevision: expectedRevision, Policy: policy,
		Kind: runtimeconfig.ChangeApply, Reason: reason, Actor: actor,
		ResetOrdinarySessionLocks: resetOrdinaryLocks,
	})
	if err != nil {
		return RoutingPolicyOverview{}, err
	}
	prospectiveResult := prepared.Prospective()
	prospective := aggregate
	prospective.State = prospectiveResult.State
	prospective.Active = &prospectiveResult.Active
	if _, err := service.coordinator.Publish(ctx, gateway.RuntimePublication{
		Prospective: prospective,
		Commit:      prepared.Commit,
	}); err != nil {
		return RoutingPolicyOverview{}, err
	}
	return service.Overview(ctx, profileID)
}

func (service *RoutingPolicyService) Rollback(
	ctx context.Context,
	profileID int64,
	expectedRevision int64,
	versionID int64,
	reason string,
) (RoutingPolicyOverview, error) {
	if service.coordinator == nil {
		return RoutingPolicyOverview{}, errors.New("routing Policy runtime coordinator is unavailable")
	}
	aggregate, err := service.store.Load(ctx, profileID)
	if err != nil {
		return RoutingPolicyOverview{}, err
	}
	if aggregate.State.Revision != expectedRevision {
		return RoutingPolicyOverview{}, runtimeconfig.ErrRevisionConflict
	}
	target, err := service.store.Policy(ctx, profileID, versionID)
	if err != nil {
		return RoutingPolicyOverview{}, err
	}
	resetOrdinaryLocks, err := ordinarySessionLockThresholdChanged(aggregate, target.Policy)
	if err != nil {
		return RoutingPolicyOverview{}, fmt.Errorf("%w: %v", runtimeconfig.ErrRollbackIncompatible, err)
	}
	prepared, err := service.store.PreparePolicy(ctx, runtimeconfig.ApplyPolicyInput{
		ProfileID: profileID, ExpectedRevision: expectedRevision, Policy: target.Policy,
		SourceVersionID: target.ID, Kind: runtimeconfig.ChangeRollback,
		Reason: reason, Actor: "admin", ResetOrdinarySessionLocks: resetOrdinaryLocks,
	})
	if err != nil {
		return RoutingPolicyOverview{}, err
	}
	prospectiveResult := prepared.Prospective()
	prospective := aggregate
	prospective.State = prospectiveResult.State
	prospective.Active = &prospectiveResult.Active
	if _, err := service.coordinator.Publish(ctx, gateway.RuntimePublication{
		Prospective: prospective,
		Commit:      prepared.Commit,
	}); err != nil {
		return RoutingPolicyOverview{}, err
	}
	return service.Overview(ctx, profileID)
}

func ordinarySessionLockThresholdChanged(
	aggregate runtimeconfig.Aggregate,
	nextPolicy profile.RoutingPolicyConfig,
) (bool, error) {
	nextRuntime, err := profile.ResolvePolicyRuntime(aggregate.Profile, aggregate.Models, &nextPolicy)
	if err != nil {
		return false, err
	}
	if aggregate.Active == nil {
		return false, nil
	}
	currentRuntime, err := profile.ResolvePolicyRuntime(
		aggregate.Profile,
		aggregate.Models,
		&aggregate.Active.Policy,
	)
	if err != nil {
		return false, err
	}
	return currentRuntime.AutoRouting.SessionLockTokenThreshold !=
		nextRuntime.AutoRouting.SessionLockTokenThreshold, nil
}

type RoutingPolicyService struct {
	store       *runtimeconfig.Store
	coordinator *gateway.RuntimeCoordinator
	compiler    *strategycompiler.Compiler
	reconciler  *RoutingPolicyReconciler
}

func (service *RoutingPolicyService) EnableGeneration(compiler *strategycompiler.Compiler) {
	service.compiler = compiler
}

func (service *RoutingPolicyService) EnableAutomaticReconciliation(
	reconciler *RoutingPolicyReconciler,
) {
	service.reconciler = reconciler
}

func NewRoutingPolicyService(
	store *runtimeconfig.Store,
	coordinator *gateway.RuntimeCoordinator,
) *RoutingPolicyService {
	return &RoutingPolicyService{store: store, coordinator: coordinator}
}

func (service *RoutingPolicyService) Overview(
	ctx context.Context,
	profileID int64,
) (RoutingPolicyOverview, error) {
	aggregate, err := service.store.Load(ctx, profileID)
	if err != nil {
		return RoutingPolicyOverview{}, err
	}
	history, err := service.store.History(ctx, profileID, 100)
	if err != nil {
		return RoutingPolicyOverview{}, err
	}
	entries := make([]RoutingPolicyHistoryEntry, 0, len(history))
	for _, version := range history {
		entry := RoutingPolicyHistoryEntry{
			PolicyVersion:      version,
			RollbackCompatible: true,
		}
		if _, err := profile.ResolvePolicyRuntime(aggregate.Profile, aggregate.Models, &version.Policy); err != nil {
			entry.RollbackCompatible = false
			entry.CompatibilityError = err.Error()
		}
		entries = append(entries, entry)
	}
	automaticUpdate := RoutingPolicyReconcileStatus{ProfileID: profileID}
	if service.reconciler != nil {
		automaticUpdate, err = service.reconciler.Status(ctx, profileID)
		if err != nil {
			return RoutingPolicyOverview{}, err
		}
	}
	return RoutingPolicyOverview{
		RuntimeState:    aggregate.State,
		Active:          aggregate.Active,
		History:         entries,
		AutomaticUpdate: automaticUpdate,
	}, nil
}
