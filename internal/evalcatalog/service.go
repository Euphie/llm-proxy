package evalcatalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const catalogFilename = "model-evaluations.json"

type Service struct {
	mu      sync.RWMutex
	current Catalog
	path    string
}

func NewService(dataDir string) (*Service, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, errors.New("evaluation catalog data directory is required")
	}
	builtIn, err := decodeCatalog(embeddedCatalog)
	if err != nil {
		return nil, fmt.Errorf("load embedded evaluation catalog: %w", err)
	}
	service := &Service{
		current: builtIn,
		path:    filepath.Join(dataDir, catalogFilename),
	}
	contents, err := os.ReadFile(service.path)
	if err == nil {
		if saved, decodeErr := decodeCatalog(contents); decodeErr == nil {
			service.current = saved
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read saved evaluation catalog: %w", err)
	}
	return service, nil
}

func (service *Service) Current() Catalog {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return cloneCatalog(service.current)
}

func (service *Service) Reload() (Catalog, error) {
	contents, err := os.ReadFile(service.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return service.Current(), nil
		}
		return Catalog{}, fmt.Errorf("read saved evaluation catalog: %w", err)
	}
	next, err := decodeCatalog(contents)
	if err != nil {
		return Catalog{}, err
	}
	service.mu.Lock()
	service.current = next
	service.mu.Unlock()
	return cloneCatalog(next), nil
}

func (service *Service) Replace(raw []byte) (Catalog, error) {
	next, err := decodeCatalog(raw)
	if err != nil {
		return Catalog{}, err
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if err := persistCatalog(service.path, next); err != nil {
		return Catalog{}, err
	}
	service.current = next
	return cloneCatalog(next), nil
}

func persistCatalog(path string, catalog Catalog) error {
	contents, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		return fmt.Errorf("encode evaluation catalog: %w", err)
	}
	contents = append(contents, '\n')
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create evaluation catalog directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".model-evaluations-*.tmp")
	if err != nil {
		return fmt.Errorf("create evaluation catalog temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	remove := true
	defer func() {
		if remove {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure evaluation catalog temporary file: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write evaluation catalog temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync evaluation catalog temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close evaluation catalog temporary file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace evaluation catalog: %w", err)
	}
	remove = false
	return nil
}

func WriteCatalog(path string, catalog Catalog) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("evaluation catalog output path is required")
	}
	if err := catalog.Validate(); err != nil {
		return err
	}
	return persistCatalog(path, catalog)
}
