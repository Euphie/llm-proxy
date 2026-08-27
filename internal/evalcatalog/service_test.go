package evalcatalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewServiceUsesEmbeddedCatalogAndSavedFilePrecedence(t *testing.T) {
	dataDir := t.TempDir()
	service, err := NewService(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	embedded := service.Current()
	if embedded.SchemaVersion != 1 || len(embedded.Sources) == 0 {
		t.Fatalf("embedded catalog=%+v", embedded)
	}

	want := catalogFixture("saved")
	if err := os.WriteFile(filepath.Join(dataDir, catalogFilename), marshalCatalogFixture(t, want), 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewService(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Current(); got.Sources[0].Version != "saved" {
		t.Fatalf("saved catalog not loaded: %+v", got.Sources[0])
	}
}

func TestNewServiceFallsBackFromCorruptSavedFile(t *testing.T) {
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, catalogFilename), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if service.Current().SchemaVersion != 1 {
		t.Fatalf("fallback catalog=%+v", service.Current())
	}
}

func TestReloadWithoutSavedFileKeepsEmbeddedCatalog(t *testing.T) {
	service, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	want := service.Current()
	got, err := service.Reload()
	if err != nil {
		t.Fatal(err)
	}
	if got.Revision != want.Revision || len(got.Sources) != len(want.Sources) {
		t.Fatalf("reloaded=%+v want=%+v", got, want)
	}
}

func TestCurrentReturnsImmutableSnapshot(t *testing.T) {
	service, err := NewService(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first := service.Current()
	first.Sources[0].Name = "mutated"
	first.Results = append(first.Results, Result{ModelID: "bad"})
	second := service.Current()
	if second.Sources[0].Name == "mutated" || len(second.Results) == len(first.Results) {
		t.Fatalf("Current exposed mutable state: %+v", second)
	}
}

func TestReplacePersistsAndActivatesAtomically(t *testing.T) {
	dataDir := t.TempDir()
	service, err := NewService(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	want := catalogFixture("replacement")
	got, err := service.Replace(marshalCatalogFixture(t, want))
	if err != nil {
		t.Fatal(err)
	}
	if got.Sources[0].Version != "replacement" || service.Current().Sources[0].Version != "replacement" {
		t.Fatalf("replacement not activated: %+v", got)
	}
	info, err := os.Stat(filepath.Join(dataDir, catalogFilename))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("catalog mode=%#o", info.Mode().Perm())
	}
}

func TestReplaceAndReloadFailurePreserveCurrentCatalog(t *testing.T) {
	dataDir := t.TempDir()
	service, err := NewService(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	before := service.Current().Revision
	if _, err := service.Replace([]byte("{")); err == nil {
		t.Fatal("Replace accepted malformed JSON")
	}
	if service.Current().Revision != before {
		t.Fatal("failed Replace changed current catalog")
	}

	if err := os.WriteFile(filepath.Join(dataDir, catalogFilename), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Reload(); err == nil {
		t.Fatal("Reload accepted malformed saved catalog")
	}
	if service.Current().Revision != before {
		t.Fatal("failed Reload changed current catalog")
	}
}

func TestNewServiceRequiresDataDirectory(t *testing.T) {
	if _, err := NewService("  "); err == nil || !strings.Contains(err.Error(), "data directory") {
		t.Fatalf("NewService error=%v", err)
	}
}
