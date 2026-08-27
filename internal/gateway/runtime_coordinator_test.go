package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Euphie/llm-proxy/internal/profile"
	"github.com/Euphie/llm-proxy/internal/runtimeconfig"
)

func TestRuntimeCoordinatorPublishesOnlyAfterExactCommitSucceeds(t *testing.T) {
	record := runtimeCoordinatorRecord()
	registry := NewRegistry()
	if err := registry.Load([]profile.Record{record}, record.ID, func(profile.Record) (http.Handler, error) {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Runtime", "old")
			w.WriteHeader(http.StatusNoContent)
		}), nil
	}); err != nil {
		t.Fatal(err)
	}
	coordinator := NewRuntimeCoordinator(registry, func(aggregate runtimeconfig.Aggregate) (http.Handler, error) {
		if aggregate.State.Revision != 2 || aggregate.Active == nil || aggregate.Active.ID != 9 {
			t.Fatalf("aggregate=%+v", aggregate)
		}
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Runtime", "new")
			w.WriteHeader(http.StatusNoContent)
		}), nil
	})
	active := runtimeconfig.PolicyVersion{ID: 9, ProfileID: record.ID}
	prospective := runtimeconfig.Aggregate{
		Profile: record,
		Active:  &active,
		State: runtimeconfig.State{
			ProfileID: record.ID, Revision: 2, ActivePolicyVersionID: active.ID,
			ModelCatalogRevision: 1,
		},
	}
	commits := 0
	result, err := coordinator.Publish(context.Background(), RuntimePublication{
		Prospective: prospective,
		Commit: func(context.Context) (runtimeconfig.ApplyPolicyResult, error) {
			commits++
			return runtimeconfig.ApplyPolicyResult{State: prospective.State, Active: active}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if commits != 1 || result.State.Revision != 2 {
		t.Fatalf("commits=%d result=%+v", commits, result)
	}
	assertRegistryRuntimeHeader(t, registry, record.ID, "new")
}

func TestRuntimeCoordinatorKeepsOldRuntimeWhenBuildOrCommitFails(t *testing.T) {
	record := runtimeCoordinatorRecord()
	for _, test := range []struct {
		name      string
		buildErr  error
		commitErr error
		commits   int
	}{
		{name: "build", buildErr: errors.New("invalid runtime"), commits: 0},
		{name: "commit", commitErr: runtimeconfig.ErrRevisionConflict, commits: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry := NewRegistry()
			if err := registry.Load([]profile.Record{record}, record.ID, func(profile.Record) (http.Handler, error) {
				return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("X-Runtime", "old")
					w.WriteHeader(http.StatusNoContent)
				}), nil
			}); err != nil {
				t.Fatal(err)
			}
			coordinator := NewRuntimeCoordinator(registry, func(runtimeconfig.Aggregate) (http.Handler, error) {
				if test.buildErr != nil {
					return nil, test.buildErr
				}
				return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("X-Runtime", "new")
					w.WriteHeader(http.StatusNoContent)
				}), nil
			})
			active := runtimeconfig.PolicyVersion{ID: 9, ProfileID: record.ID}
			prospective := runtimeconfig.Aggregate{
				Profile: record, Active: &active,
				State: runtimeconfig.State{
					ProfileID: record.ID, Revision: 2, ActivePolicyVersionID: 9,
					ModelCatalogRevision: 1,
				},
			}
			commits := 0
			_, err := coordinator.Publish(context.Background(), RuntimePublication{
				Prospective: prospective,
				Commit: func(context.Context) (runtimeconfig.ApplyPolicyResult, error) {
					commits++
					return runtimeconfig.ApplyPolicyResult{}, test.commitErr
				},
			})
			if err == nil || commits != test.commits {
				t.Fatalf("error=%v commits=%d", err, commits)
			}
			assertRegistryRuntimeHeader(t, registry, record.ID, "old")
		})
	}
}

func assertRegistryRuntimeHeader(t *testing.T, registry *Registry, profileID int64, want string) {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	response := httptest.NewRecorder()
	registry.load().byID[profileID].handler.ServeHTTP(response, request)
	if got := response.Header().Get("X-Runtime"); got != want {
		t.Fatalf("runtime header=%q want=%q", got, want)
	}
}

func runtimeCoordinatorRecord() profile.Record {
	return profile.Record{
		ID: 7, Slug: "runtime", DisplayName: "Runtime", Enabled: true,
		Config: profile.NewConfig(profile.ProtocolAnthropic, "https://example.com"),
	}
}
