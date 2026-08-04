package modelcatalog

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewServiceUsesEmbeddedCatalogAndFallsBackFromCorruptSavedFile(t *testing.T) {
	dataDir := t.TempDir()
	service, err := NewService(dataDir, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	builtIn := service.Current()
	if builtIn.Source.Name != "Models.dev" || len(builtIn.Models) < 200 {
		t.Fatalf("built-in catalog=%+v models=%d", builtIn.Source, len(builtIn.Models))
	}

	if err := os.WriteFile(filepath.Join(dataDir, catalogFilename), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewService(dataDir, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Current().Source.Revision != builtIn.Source.Revision {
		t.Fatalf("corrupt saved catalog replaced fallback: %+v", reopened.Current().Source)
	}
}

func TestNewServiceLoadsValidSavedCatalog(t *testing.T) {
	dataDir := t.TempDir()
	want := catalogFixture("saved")
	contents, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, catalogFilename), contents, 0o600); err != nil {
		t.Fatal(err)
	}

	service, err := NewService(dataDir, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	got := service.Current()
	if got.Source.Revision != want.Source.Revision || got.Models[0].CanonicalID != "acme/alpha" {
		t.Fatalf("catalog=%+v", got)
	}
}

func TestRefreshFailurePreservesCurrentCatalog(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, context.DeadlineExceeded
	})}
	service, err := NewService(t.TempDir(), client)
	if err != nil {
		t.Fatal(err)
	}
	before := service.Current().Source.Revision
	if _, err := service.Refresh(context.Background()); err == nil {
		t.Fatal("Refresh succeeded")
	}
	if after := service.Current().Source.Revision; after != before {
		t.Fatalf("revision changed after failure: %s -> %s", before, after)
	}
}

func TestRefreshDownloadsFixedFeedsPersistsAndActivatesCatalog(t *testing.T) {
	requested := make([]string, 0, 2)
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		requested = append(requested, request.URL.String())
		body := canonicalFeedFixture
		if request.URL.String() == providersURL {
			body = providerFeedFixture
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
			Request:    request,
		}, nil
	})}
	dataDir := t.TempDir()
	service, err := NewService(dataDir, client)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC) }
	previous := service.Current().Source

	result, err := service.Refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed || result.PreviousSource.Revision != previous.Revision {
		t.Fatalf("result=%+v", result)
	}
	if len(requested) != 2 || requested[0] != modelsURL || requested[1] != providersURL {
		t.Fatalf("requested=%v", requested)
	}
	if service.Current().Models[0].CanonicalID != "zhipuai/glm-5.2" {
		t.Fatalf("current=%+v", service.Current())
	}
	info, err := os.Stat(filepath.Join(dataDir, catalogFilename))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("catalog mode=%#o", info.Mode().Perm())
	}

	reopened, err := NewService(dataDir, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Current().Source.Revision != result.Catalog.Source.Revision {
		t.Fatalf("reopened=%+v", reopened.Current().Source)
	}
}

func TestRefreshRejectsOversizedFeedAndPreservesCurrentCatalog(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(bytes.NewReader(make([]byte, maxFeedBytes+1))),
			Header:     make(http.Header),
			Request:    request,
		}, nil
	})}
	service, err := NewService(t.TempDir(), client)
	if err != nil {
		t.Fatal(err)
	}
	before := service.Current().Source.Revision
	if _, err := service.Refresh(context.Background()); err == nil {
		t.Fatal("Refresh accepted oversized feed")
	}
	if service.Current().Source.Revision != before {
		t.Fatal("oversized refresh changed current catalog")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func catalogFixture(seed string) Catalog {
	digest := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	return Catalog{
		Source: Source{
			Name:            "Models.dev",
			Revision:        digest,
			ModelsSHA256:    digest,
			ProvidersSHA256: digest,
			Retrieved:       "2026-08-02",
		},
		Models: []Model{{
			ID:             "alpha",
			CanonicalID:    "acme/alpha",
			Aliases:        []string{"acme/alpha"},
			Provider:       "Acme",
			Name:           seed,
			Family:         "alpha",
			SupportsVision: boolPointer(false),
			SupportsTools:  boolPointer(true),
			Lifecycle:      "stable",
		}},
	}
}
