package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sync"

	"github.com/Euphie/llm-proxy/internal/profile"
	"github.com/Euphie/llm-proxy/internal/runtimeconfig"
)

type RuntimeBuilder func(runtimeconfig.Aggregate) (http.Handler, error)

type RuntimePublication struct {
	Prospective      runtimeconfig.Aggregate
	DefaultProfileID int64
	Commit           func(context.Context) (runtimeconfig.ApplyPolicyResult, error)
}

type RuntimeCoordinator struct {
	mu       sync.Mutex
	registry *Registry
	build    RuntimeBuilder
}

func NewRuntimeCoordinator(registry *Registry, build RuntimeBuilder) *RuntimeCoordinator {
	return &RuntimeCoordinator{registry: registry, build: build}
}

func (coordinator *RuntimeCoordinator) Publish(
	ctx context.Context,
	publication RuntimePublication,
) (runtimeconfig.ApplyPolicyResult, error) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.registry == nil || coordinator.build == nil || publication.Commit == nil {
		return runtimeconfig.ApplyPolicyResult{}, fmt.Errorf("%w: runtime publication is not configured", ErrRuntimeSync)
	}
	prospective := cloneAggregate(publication.Prospective)
	expected := cloneAggregate(publication.Prospective)
	handler, err := coordinator.build(prospective)
	if err != nil {
		return runtimeconfig.ApplyPolicyResult{}, fmt.Errorf("%w: prepare runtime: %w", ErrRuntimeSync, err)
	}
	if !reflect.DeepEqual(prospective, expected) {
		return runtimeconfig.ApplyPolicyResult{}, fmt.Errorf("%w: runtime builder mutated prospective state", ErrRuntimeSync)
	}
	current := coordinator.registry.load()
	defaultID := current.defaultID
	if publication.DefaultProfileID > 0 {
		defaultID = publication.DefaultProfileID
	}
	if defaultID == 0 {
		defaultID = prospective.Profile.ID
	}
	prepared, err := coordinator.registry.prepareUpsert(
		prospective.Profile,
		defaultID,
		func(_ profile.Record) (http.Handler, error) { return handler, nil },
	)
	if err != nil {
		return runtimeconfig.ApplyPolicyResult{}, fmt.Errorf("%w: prepare registry: %w", ErrRuntimeSync, err)
	}
	committed, err := publication.Commit(ctx)
	if err != nil {
		return runtimeconfig.ApplyPolicyResult{}, err
	}
	coordinator.registry.publishPrepared(prepared)
	return committed, nil
}

func cloneAggregate(source runtimeconfig.Aggregate) runtimeconfig.Aggregate {
	payload, err := json.Marshal(source)
	if err != nil {
		panic(err)
	}
	var cloned runtimeconfig.Aggregate
	if err := json.Unmarshal(payload, &cloned); err != nil {
		panic(err)
	}
	return cloned
}
