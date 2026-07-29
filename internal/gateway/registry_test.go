package gateway

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/Euphie/llm-proxy/internal/profile"
)

func TestRegistryLoadPublishesOnlyAfterEveryEnabledProfileBuilds(t *testing.T) {
	registry := NewRegistry()
	old := testRecord(1, "old", true)
	if err := registry.Load([]profile.Record{old}, old.ID, responseBuilder(nil)); err != nil {
		t.Fatal(err)
	}
	before := registry.current.Load()

	buildErr := errors.New("build failed")
	var builds atomic.Int32
	records := []profile.Record{
		testRecord(2, "first", true),
		testRecord(3, "disabled", false),
		testRecord(4, "broken", true),
	}
	err := registry.Load(records, 2, func(record profile.Record) (http.Handler, error) {
		builds.Add(1)
		if record.ID == 4 {
			return nil, buildErr
		}
		return responseHandler(record.Slug), nil
	})
	if !errors.Is(err, buildErr) {
		t.Fatalf("Load() error = %v, want %v", err, buildErr)
	}
	if builds.Load() != 2 {
		t.Fatalf("builder called %d times, want 2", builds.Load())
	}
	if got := registry.current.Load(); got != before {
		t.Fatal("failed Load published a new snapshot")
	}
}

func TestRegistryLoadKeepsDisabledProfilesAddressableWithoutBuilding(t *testing.T) {
	registry := NewRegistry()
	record := testRecord(1, "paused", false)
	var builds atomic.Int32
	if err := registry.Load([]profile.Record{record}, record.ID, func(profile.Record) (http.Handler, error) {
		builds.Add(1)
		return responseHandler("unexpected"), nil
	}); err != nil {
		t.Fatal(err)
	}

	got := registry.current.Load().byID[record.ID]
	if got == nil || !reflect.DeepEqual(got.record, record) || got.handler != nil {
		t.Fatalf("disabled runtime = %#v", got)
	}
	if builds.Load() != 0 {
		t.Fatalf("builder called %d times, want 0", builds.Load())
	}
}

func TestRegistryUpsertRebuildsOnlyChangedProfileAndRemovesOldSlug(t *testing.T) {
	registry := NewRegistry()
	records := []profile.Record{
		testRecord(1, "default", true),
		testRecord(2, "coding", true),
	}
	if err := registry.Load(records, 1, responseBuilder(nil)); err != nil {
		t.Fatal(err)
	}
	before := registry.current.Load()
	unchanged := before.byID[1]
	replaced := before.byID[2]

	var builds atomic.Int32
	updated := testRecord(2, "dev", true)
	if err := registry.Upsert(updated, 2, func(record profile.Record) (http.Handler, error) {
		builds.Add(1)
		return responseHandler("new " + record.Slug), nil
	}); err != nil {
		t.Fatal(err)
	}

	after := registry.current.Load()
	if after == before {
		t.Fatal("Upsert did not publish a new snapshot")
	}
	if after.defaultID != 2 {
		t.Fatalf("defaultID = %d, want 2", after.defaultID)
	}
	if builds.Load() != 1 {
		t.Fatalf("builder called %d times, want 1", builds.Load())
	}
	if after.byID[1] != unchanged || after.bySlug["default"] != unchanged {
		t.Fatal("Upsert replaced an unchanged runtime")
	}
	if after.byID[2] == replaced || after.bySlug["dev"] != after.byID[2] {
		t.Fatal("Upsert did not install the changed runtime")
	}
	if _, exists := after.bySlug["coding"]; exists {
		t.Fatal("Upsert retained the previous slug")
	}
}

func TestRegistryUpsertFailureLeavesSnapshotUnchanged(t *testing.T) {
	registry := NewRegistry()
	record := testRecord(1, "default", true)
	if err := registry.Load([]profile.Record{record}, record.ID, responseBuilder(nil)); err != nil {
		t.Fatal(err)
	}
	before := registry.current.Load()
	buildErr := errors.New("build failed")

	err := registry.Upsert(testRecord(1, "renamed", true), 1, func(profile.Record) (http.Handler, error) {
		return nil, buildErr
	})
	if !errors.Is(err, buildErr) {
		t.Fatalf("Upsert() error = %v, want %v", err, buildErr)
	}
	if got := registry.current.Load(); got != before {
		t.Fatal("failed Upsert published a new snapshot")
	}
}

func TestRegistryDeleteRemovesProfileAndPreservesOtherRuntime(t *testing.T) {
	registry := NewRegistry()
	records := []profile.Record{
		testRecord(1, "default", true),
		testRecord(2, "coding", true),
	}
	if err := registry.Load(records, 1, responseBuilder(nil)); err != nil {
		t.Fatal(err)
	}
	unchanged := registry.current.Load().byID[1]

	registry.Delete(2, 1)

	got := registry.current.Load()
	if got.byID[1] != unchanged || got.bySlug["default"] != unchanged {
		t.Fatal("Delete replaced an unchanged runtime")
	}
	if _, exists := got.byID[2]; exists {
		t.Fatal("Delete retained the Profile ID")
	}
	if _, exists := got.bySlug["coding"]; exists {
		t.Fatal("Delete retained the Profile slug")
	}
}

func TestRegistrySetDefaultReusesExistingRuntimes(t *testing.T) {
	registry := NewRegistry()
	records := []profile.Record{
		testRecord(1, "default", true),
		testRecord(2, "coding", true),
	}
	handlers := map[int64]*identityHandler{}
	if err := registry.Load(records, 1, func(record profile.Record) (http.Handler, error) {
		handler := &identityHandler{body: record.Slug}
		handlers[record.ID] = handler
		return handler, nil
	}); err != nil {
		t.Fatal(err)
	}
	before := registry.current.Load()

	if err := registry.SetDefault(2); err != nil {
		t.Fatal(err)
	}

	after := registry.current.Load()
	if after == before {
		t.Fatal("SetDefault did not publish a new snapshot")
	}
	if after.defaultID != 2 {
		t.Fatalf("defaultID=%d, want 2", after.defaultID)
	}
	for _, id := range []int64{1, 2} {
		if after.byID[id] != before.byID[id] {
			t.Fatalf("Profile %d runtime pointer changed", id)
		}
		if after.byID[id].handler != handlers[id] {
			t.Fatalf("Profile %d handler identity changed", id)
		}
	}
}

func TestRegistryInFlightRequestKeepsOldRuntime(t *testing.T) {
	registry := NewRegistry()
	record := testRecord(1, "default", true)
	entered := make(chan struct{})
	release := make(chan struct{})
	oldHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		_, _ = io.WriteString(w, "old")
	})
	if err := registry.Load([]profile.Record{record}, record.ID, func(profile.Record) (http.Handler, error) {
		return oldHandler, nil
	}); err != nil {
		t.Fatal(err)
	}
	router := NewRouter(registry)

	firstResponse := httptest.NewRecorder()
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		router.ServeHTTP(firstResponse, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	}()
	<-entered

	if err := registry.Upsert(record, record.ID, func(profile.Record) (http.Handler, error) {
		return responseHandler("new"), nil
	}); err != nil {
		t.Fatal(err)
	}
	secondResponse := httptest.NewRecorder()
	router.ServeHTTP(secondResponse, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	if secondResponse.Code != http.StatusOK || secondResponse.Body.String() != "new" {
		t.Fatalf("new request: status=%d body=%q", secondResponse.Code, secondResponse.Body.String())
	}

	close(release)
	<-firstDone
	if firstResponse.Code != http.StatusOK || firstResponse.Body.String() != "old" {
		t.Fatalf("old request: status=%d body=%q", firstResponse.Code, firstResponse.Body.String())
	}
}

func testRecord(id int64, slug string, enabled bool) profile.Record {
	return profile.Record{ID: id, Slug: slug, DisplayName: slug, Enabled: enabled}
}

func responseBuilder(builds *atomic.Int32) Builder {
	return func(record profile.Record) (http.Handler, error) {
		if builds != nil {
			builds.Add(1)
		}
		return responseHandler(record.Slug), nil
	}
}

func responseHandler(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	})
}

type identityHandler struct {
	body string
}

func (h *identityHandler) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	_, _ = io.WriteString(w, h.body)
}
