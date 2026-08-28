package admin

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Euphie/llm-proxy/internal/database"
	"github.com/Euphie/llm-proxy/internal/gateway"
	"github.com/Euphie/llm-proxy/internal/profile"
)

var errUnexpectedProfileMutation = errors.New("unexpected profile mutation")

// Break caught: returning from Save after the database commit without publishing the committed runtime.
func TestProfileServicePublishesOnlyAfterDatabaseCommit(t *testing.T) {
	service, registry := newProfileServiceFixture(t)

	saved, err := service.Save(context.Background(), profile.SaveInput{
		Slug: "coding", DisplayName: "Coding", Enabled: true,
		Config: profile.NewConfig(profile.ProtocolAnthropic, "https://one.example"),
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID == 0 {
		t.Fatal("saved Profile has no ID")
	}
	assertProfileRoute(t, registry, "/v1/messages", http.StatusOK, "https://one.example")
}

// Break caught: publishing a failed duplicate-slug update or replacing the last valid runtime.
func TestProfileServiceDuplicateSlugFailureLeavesRuntimeUnchanged(t *testing.T) {
	service, registry := newProfileServiceFixture(t)
	ctx := context.Background()
	first := saveServiceProfile(t, service, "first", "https://first.example", true)
	second := saveServiceProfile(t, service, "second", "https://second.example", false)

	_, err := service.Save(ctx, profile.SaveInput{
		ID: second.ID, Slug: first.Slug, DisplayName: "Duplicate", Enabled: true,
		Config: profile.NewConfig(profile.ProtocolAnthropic, "https://changed.example"),
	}, false)
	if !errors.Is(err, profile.ErrSlugConflict) {
		t.Fatalf("Save error=%v, want ErrSlugConflict", err)
	}

	assertProfileRoute(t, registry, "/second/v1/messages", http.StatusOK, "https://second.example")
	assertProfileRoute(t, registry, "/v1/messages", http.StatusOK, "https://first.example")
}

// Break caught: persisting a copied profile without making its slug immediately routable.
func TestProfileServiceCopyPublishesNewSlug(t *testing.T) {
	service, registry := newProfileServiceFixture(t)
	source := saveServiceProfile(t, service, "coding", "https://coding.example", true)

	copied, err := service.Copy(context.Background(), source.ID, "coding-copy", "Coding Copy")
	if err != nil {
		t.Fatal(err)
	}
	if copied.ID == source.ID || copied.Slug != "coding-copy" {
		t.Fatalf("copied=%+v", copied)
	}
	assertProfileRoute(t, registry, "/coding-copy/v1/messages", http.StatusOK, "https://coding.example")
}

// Break caught: changing the persisted default without switching root routing to the same profile.
func TestProfileServiceSetDefaultSwitchesRootRouting(t *testing.T) {
	service, registry := newProfileServiceFixture(t)
	saveServiceProfile(t, service, "first", "https://first.example", true)
	second := saveServiceProfile(t, service, "second", "https://second.example", false)

	if err := service.SetDefault(context.Background(), second.ID); err != nil {
		t.Fatal(err)
	}
	assertProfileRoute(t, registry, "/v1/messages", http.StatusOK, "https://second.example")
}

// Break caught: deleting the database row while leaving its old slug routable or the root route stale.
func TestProfileServiceDeleteRemovesOldSlug(t *testing.T) {
	service, registry := newProfileServiceFixture(t)
	first := saveServiceProfile(t, service, "first", "https://first.example", true)
	second := saveServiceProfile(t, service, "second", "https://second.example", false)

	if err := service.Delete(context.Background(), first.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	assertProfileRoute(t, registry, "/first/v1/messages", http.StatusNotFound, "")
	assertProfileRoute(t, registry, "/v1/messages", http.StatusOK, "https://second.example")
}

// Break caught: serving stale or incomplete reads instead of the Store snapshot and record.
func TestProfileServiceListAndGetUseStore(t *testing.T) {
	service, _ := newProfileServiceFixture(t)
	first := saveServiceProfile(t, service, "first", "https://first.example", true)
	second := saveServiceProfile(t, service, "second", "https://second.example", false)

	records, defaultID, err := service.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].ID != first.ID || records[1].ID != second.ID ||
		defaultID != first.ID {
		t.Fatalf("records=%+v defaultID=%d", records, defaultID)
	}
	got, err := service.Get(context.Background(), second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != second.ID || got.Slug != "second" {
		t.Fatalf("got=%+v", got)
	}
}

// Break caught: sending unresolved profile data to the process-wide mutation coordinator.
func TestProfileServiceResolvesRecordsBeforeEveryMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(context.Context, *ProfileService, *sql.DB, int64) error
	}{
		{
			name: "save",
			mutate: func(ctx context.Context, service *ProfileService, _ *sql.DB, _ int64) error {
				_, err := service.Save(ctx, profile.SaveInput{
					Slug: "Bad Slug", DisplayName: "Bad", Enabled: true,
					Config: profile.NewConfig(profile.ProtocolAnthropic, "https://bad.example"),
				}, false)
				return err
			},
		},
		{
			name: "copy",
			mutate: func(ctx context.Context, service *ProfileService, _ *sql.DB, id int64) error {
				_, err := service.Copy(ctx, id, "Bad Slug", "Bad Copy")
				return err
			},
		},
		{
			name: "set default",
			mutate: func(ctx context.Context, service *ProfileService, db *sql.DB, id int64) error {
				corruptServiceProfileSlug(t, db, id)
				return service.SetDefault(ctx, id)
			},
		},
		{
			name: "delete",
			mutate: func(ctx context.Context, service *ProfileService, db *sql.DB, id int64) error {
				corruptServiceProfileSlug(t, db, id)
				return service.Delete(ctx, id, 0)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db, err := database.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			store := profile.NewStore(db)
			record, err := store.Save(context.Background(), profile.SaveInput{
				Slug: "valid", DisplayName: "Valid", Enabled: true,
				Config: profile.NewConfig(profile.ProtocolAnthropic, "https://valid.example"),
			}, true)
			if err != nil {
				t.Fatal(err)
			}
			mutations := &rejectingProfileMutationStore{}
			coordinator := gateway.NewCoordinator(mutations, gateway.NewRegistry(), func(profile.Record) (http.Handler, error) {
				return http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), nil
			})
			service := NewProfileService(store, coordinator, nil)

			err = tt.mutate(context.Background(), service, db, record.ID)
			if !errors.Is(err, profile.ErrInvalidSlug) {
				t.Fatalf("mutation error=%v, want ErrInvalidSlug", err)
			}
		})
	}
}

type rejectingProfileMutationStore struct{}

func (s *rejectingProfileMutationStore) Save(
	context.Context,
	profile.SaveInput,
	bool,
) (profile.Record, error) {
	return profile.Record{}, errUnexpectedProfileMutation
}

func (s *rejectingProfileMutationStore) LoadSnapshot(context.Context) ([]profile.Record, int64, error) {
	return nil, 0, errUnexpectedProfileMutation
}

func (s *rejectingProfileMutationStore) Copy(
	context.Context,
	int64,
	string,
	string,
) (profile.Record, error) {
	return profile.Record{}, errUnexpectedProfileMutation
}

func (s *rejectingProfileMutationStore) SetDefault(context.Context, int64) error {
	return errUnexpectedProfileMutation
}

func (s *rejectingProfileMutationStore) Delete(context.Context, int64, int64) error {
	return errUnexpectedProfileMutation
}

func corruptServiceProfileSlug(t *testing.T, db *sql.DB, id int64) {
	t.Helper()
	_, err := db.Exec(`UPDATE profiles SET slug = ? WHERE id = ?`, "Bad Slug", id)
	if err != nil {
		t.Fatal(err)
	}
}

func newProfileServiceFixture(t *testing.T) (*ProfileService, *gateway.Registry) {
	t.Helper()
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	store := profile.NewStore(db)
	registry := gateway.NewRegistry()
	builder := func(record profile.Record) (http.Handler, error) {
		runtime, err := record.Resolve()
		if err != nil {
			return nil, err
		}
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, runtime.Upstream)
		}), nil
	}
	return NewProfileService(store, gateway.NewCoordinator(store, registry, builder), nil), registry
}

func saveServiceProfile(
	t *testing.T,
	service *ProfileService,
	slug, upstream string,
	makeDefault bool,
) profile.Record {
	t.Helper()
	record, err := service.Save(context.Background(), profile.SaveInput{
		Slug: slug, DisplayName: slug, Enabled: true,
		Config: profile.NewConfig(profile.ProtocolAnthropic, upstream),
	}, makeDefault)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func assertProfileRoute(
	t *testing.T,
	registry *gateway.Registry,
	path string,
	wantStatus int,
	wantBody string,
) {
	t.Helper()
	response := httptest.NewRecorder()
	gateway.NewRouter(registry).ServeHTTP(
		response,
		httptest.NewRequest(http.MethodPost, path, nil),
	)
	if response.Code != wantStatus {
		t.Fatalf("%s status=%d body=%q", path, response.Code, response.Body.String())
	}
	if wantBody != "" && response.Body.String() != wantBody {
		t.Fatalf("%s body=%q, want %q", path, response.Body.String(), wantBody)
	}
}
