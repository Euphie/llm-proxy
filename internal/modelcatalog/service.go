package modelcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	catalogFilename = "model-catalog.json"
	modelsURL       = "https://models.dev/models.json"
	providersURL    = "https://models.dev/api.json"
	maxFeedBytes    = 8 << 20
)

type RefreshResult struct {
	Catalog        Catalog `json:"catalog"`
	PreviousSource Source  `json:"previous_source"`
	Changed        bool    `json:"changed"`
}

type Service struct {
	mu      sync.RWMutex
	current Catalog
	path    string
	client  *http.Client
	now     func() time.Time
}

func NewService(dataDir string, client *http.Client) (*Service, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, errors.New("model catalog data directory is required")
	}
	builtIn, err := decodeCatalog(embeddedCatalog)
	if err != nil {
		return nil, fmt.Errorf("load embedded model catalog: %w", err)
	}
	if client == nil {
		client = http.DefaultClient
	}
	service := &Service{
		current: builtIn,
		path:    filepath.Join(dataDir, catalogFilename),
		client:  client,
		now:     time.Now,
	}
	contents, err := os.ReadFile(service.path)
	if err == nil {
		if saved, decodeErr := decodeCatalog(contents); decodeErr == nil {
			service.current = saved
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read saved model catalog: %w", err)
	}
	return service, nil
}

func (service *Service) Current() Catalog {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return cloneCatalog(service.current)
}

func (service *Service) Refresh(ctx context.Context) (RefreshResult, error) {
	models, err := service.fetch(ctx, modelsURL)
	if err != nil {
		return RefreshResult{}, err
	}
	providers, err := service.fetch(ctx, providersURL)
	if err != nil {
		return RefreshResult{}, err
	}
	next, err := BuildFromFeeds(models, providers, service.now())
	if err != nil {
		return RefreshResult{}, err
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	previous := service.current.Source
	if next.Source.Revision == previous.Revision {
		return RefreshResult{Catalog: cloneCatalog(service.current), PreviousSource: previous}, nil
	}
	if err := persistCatalog(service.path, next); err != nil {
		return RefreshResult{}, err
	}
	service.current = next
	return RefreshResult{
		Catalog:        cloneCatalog(next),
		PreviousSource: previous,
		Changed:        true,
	}, nil
}

func (service *Service) fetch(ctx context.Context, url string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	response, err := service.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("download model catalog feed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("download model catalog feed: %s", response.Status)
	}
	contents, err := io.ReadAll(io.LimitReader(response.Body, maxFeedBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read model catalog feed: %w", err)
	}
	if len(contents) > maxFeedBytes {
		return nil, errors.New("model catalog feed exceeds 8 MiB limit")
	}
	return contents, nil
}

func persistCatalog(path string, catalog Catalog) error {
	contents, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		return fmt.Errorf("encode model catalog: %w", err)
	}
	contents = append(contents, '\n')
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".model-catalog-*.tmp")
	if err != nil {
		return fmt.Errorf("create model catalog temporary file: %w", err)
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
		return fmt.Errorf("secure model catalog temporary file: %w", err)
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write model catalog temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync model catalog temporary file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close model catalog temporary file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace model catalog: %w", err)
	}
	remove = false
	return nil
}
