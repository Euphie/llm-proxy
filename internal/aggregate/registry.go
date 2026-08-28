package aggregate

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

type runtimeProvider struct {
	ID         int64
	Enabled    bool
	Protocol   string
	Upstream   string
	AuthHeader string
	Secret     string
	Models     map[string]bool
}

type runtimeRoute struct {
	PublicModel   string
	ProviderModel string
	Provider      *runtimeProvider
}

type runtimeGateway struct {
	ID          int64
	Slug        string
	DisplayName string
	Enabled     bool
	Protocol    string
	Routes      map[string]runtimeRoute
}

type runtimeKey struct {
	ID        int64
	GatewayID int64
	Name      string
	Prefix    string
	LastFour  string
	Enabled   bool
	ExpiresAt *time.Time
}

type runtimeSnapshot struct {
	byID   map[int64]*runtimeGateway
	bySlug map[string]*runtimeGateway
	keys   map[[32]byte]runtimeKey
}

type Registry struct {
	mu      sync.Mutex
	current atomic.Pointer[runtimeSnapshot]
}

func NewRegistry() *Registry {
	registry := &Registry{}
	registry.current.Store(emptyRuntimeSnapshot())
	return registry
}

func (r *Registry) Load(snapshot Snapshot) error {
	next, err := buildRuntimeSnapshot(snapshot)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.current.Store(next)
	return nil
}

func (r *Registry) Current() *runtimeSnapshot {
	if current := r.current.Load(); current != nil {
		return current
	}
	return emptyRuntimeSnapshot()
}

func buildRuntimeSnapshot(snapshot Snapshot) (*runtimeSnapshot, error) {
	providers := make(map[int64]*runtimeProvider, len(snapshot.Providers))
	for _, account := range snapshot.Providers {
		models := make([]ProviderModelInput, 0, len(account.Models))
		for _, model := range account.Models {
			models = append(models, ProviderModelInput{
				ID:          model.ID,
				ModelID:     model.ModelID,
				DisplayName: model.DisplayName,
				Enabled:     model.Enabled,
			})
		}
		normalized, err := NormalizeProviderInput(ProviderAccountInput{
			ID:          account.ID,
			Slug:        account.Slug,
			DisplayName: account.DisplayName,
			Enabled:     account.Enabled,
			Protocol:    account.Protocol,
			Upstream:    account.Upstream,
			AuthHeader:  account.AuthHeader,
			Secret:      account.Secret,
			Models:      models,
		})
		if err != nil {
			return nil, fmt.Errorf("resolve provider account %d: %w", account.ID, err)
		}
		if normalized.Secret == "" {
			return nil, fmt.Errorf("%w: provider %q secret is empty", ErrInvalidConfig, normalized.Slug)
		}
		modelState := make(map[string]bool, len(normalized.Models))
		for _, model := range normalized.Models {
			modelState[model.ModelID] = model.Enabled
		}
		providers[normalized.ID] = &runtimeProvider{
			ID:         normalized.ID,
			Enabled:    normalized.Enabled,
			Protocol:   string(normalized.Protocol),
			Upstream:   normalized.Upstream,
			AuthHeader: normalized.AuthHeader,
			Secret:     normalized.Secret,
			Models:     modelState,
		}
	}

	next := emptyRuntimeSnapshot()
	for _, gateway := range snapshot.Gateways {
		input := GatewayInput{
			ID:          gateway.ID,
			Slug:        gateway.Slug,
			DisplayName: gateway.DisplayName,
			Enabled:     gateway.Enabled,
			Protocol:    gateway.Protocol,
			Routes:      make([]RouteInput, 0, len(gateway.Routes)),
		}
		for _, route := range gateway.Routes {
			input.Routes = append(input.Routes, RouteInput{
				ID:                route.ID,
				PublicModel:       route.PublicModel,
				ProviderAccountID: route.ProviderAccountID,
				ProviderModel:     route.ProviderModel,
				Enabled:           route.Enabled,
			})
		}
		if _, err := NormalizeGatewayInput(input); err != nil {
			return nil, fmt.Errorf("resolve aggregate gateway %d: %w", gateway.ID, err)
		}
		runtime := &runtimeGateway{
			ID:          gateway.ID,
			Slug:        gateway.Slug,
			DisplayName: gateway.DisplayName,
			Enabled:     gateway.Enabled,
			Protocol:    string(gateway.Protocol),
			Routes:      make(map[string]runtimeRoute),
		}
		for _, route := range gateway.Routes {
			if !route.Enabled {
				continue
			}
			provider := providers[route.ProviderAccountID]
			if provider == nil {
				return nil, fmt.Errorf("%w: route provider account %d not found", ErrInvalidConfig, route.ProviderAccountID)
			}
			if provider.Protocol != string(gateway.Protocol) {
				return nil, fmt.Errorf("%w: route provider protocol mismatch", ErrInvalidConfig)
			}
			if !provider.Enabled {
				continue
			}
			if !provider.Models[route.ProviderModel] {
				continue
			}
			runtime.Routes[route.PublicModel] = runtimeRoute{
				PublicModel:   route.PublicModel,
				ProviderModel: route.ProviderModel,
				Provider:      provider,
			}
		}
		next.byID[gateway.ID] = runtime
		next.bySlug[gateway.Slug] = runtime
	}

	for _, key := range snapshot.Keys {
		if len(key.TokenHash) != 32 {
			return nil, fmt.Errorf("%w: invalid key hash length", ErrInvalidConfig)
		}
		var hash [32]byte
		copy(hash[:], key.TokenHash)
		next.keys[hash] = runtimeKey{
			ID:        key.ID,
			GatewayID: key.GatewayID,
			Name:      key.Name,
			Prefix:    key.Prefix,
			LastFour:  key.LastFour,
			Enabled:   key.Enabled,
			ExpiresAt: key.ExpiresAt,
		}
	}
	return next, nil
}

func emptyRuntimeSnapshot() *runtimeSnapshot {
	return &runtimeSnapshot{
		byID:   make(map[int64]*runtimeGateway),
		bySlug: make(map[string]*runtimeGateway),
		keys:   make(map[[32]byte]runtimeKey),
	}
}
