package aggregate

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Euphie/llm-proxy/internal/profile"
)

var (
	ErrInvalidConfig        = errors.New("aggregate gateway config invalid")
	ErrNotFound             = errors.New("aggregate gateway record not found")
	ErrProviderSlugConflict = errors.New("provider slug conflict")
	ErrGatewaySlugConflict  = errors.New("aggregate gateway slug conflict")
	ErrInvalidKey           = errors.New("aggregate gateway key invalid")
)

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
var headerPattern = regexp.MustCompile(`^[!#$%&'*+.^_` + "`" + `|~0-9A-Za-z-]+$`)

const KeyPrefix = "lgp_"

type ProviderAccountInput struct {
	ID          int64
	Slug        string
	DisplayName string
	Enabled     bool
	Protocol    profile.Protocol
	Upstream    string
	AuthHeader  string
	Secret      string
	Models      []ProviderModelInput
}

type ProviderModelInput struct {
	ID          int64
	ModelID     string `json:"model_id"`
	DisplayName string `json:"display_name"`
	Enabled     bool   `json:"enabled"`
}

type ProviderAccount struct {
	ID          int64            `json:"id"`
	Slug        string           `json:"slug"`
	DisplayName string           `json:"display_name"`
	Enabled     bool             `json:"enabled"`
	Protocol    profile.Protocol `json:"protocol"`
	Upstream    string           `json:"upstream"`
	AuthHeader  string           `json:"auth_header"`
	Secret      string           `json:"-"`
	Models      []ProviderModel  `json:"models"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
}

type ProviderModel struct {
	ID                int64     `json:"id"`
	ProviderAccountID int64     `json:"provider_account_id"`
	ModelID           string    `json:"model_id"`
	DisplayName       string    `json:"display_name"`
	Enabled           bool      `json:"enabled"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type ProviderAccountResponse struct {
	ID          int64            `json:"id"`
	Slug        string           `json:"slug"`
	DisplayName string           `json:"display_name"`
	Enabled     bool             `json:"enabled"`
	Protocol    profile.Protocol `json:"protocol"`
	Upstream    string           `json:"upstream"`
	AuthHeader  string           `json:"auth_header"`
	HasSecret   bool             `json:"has_secret"`
	Models      []ProviderModel  `json:"models"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
}

type RouteInput struct {
	ID                int64
	PublicModel       string `json:"public_model"`
	ProviderAccountID int64  `json:"provider_account_id"`
	ProviderModel     string `json:"provider_model"`
	Enabled           bool   `json:"enabled"`
}

type ModelRoute struct {
	ID                int64     `json:"id"`
	GatewayID         int64     `json:"gateway_id"`
	PublicModel       string    `json:"public_model"`
	ProviderAccountID int64     `json:"provider_account_id"`
	ProviderModel     string    `json:"provider_model"`
	Enabled           bool      `json:"enabled"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type GatewayInput struct {
	ID          int64
	Slug        string
	DisplayName string
	Enabled     bool
	Protocol    profile.Protocol
	Routes      []RouteInput
}

type Gateway struct {
	ID          int64            `json:"id"`
	Slug        string           `json:"slug"`
	DisplayName string           `json:"display_name"`
	Enabled     bool             `json:"enabled"`
	Protocol    profile.Protocol `json:"protocol"`
	Routes      []ModelRoute     `json:"routes"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
}

type IssuedKeyInput struct {
	GatewayID int64
	Name      string
	Enabled   bool
	ExpiresAt *time.Time
}

type IssuedKey struct {
	ID         int64      `json:"id"`
	GatewayID  int64      `json:"gateway_id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	LastFour   string     `json:"last_four"`
	TokenHash  []byte     `json:"-"`
	Enabled    bool       `json:"enabled"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	Key        string     `json:"key,omitempty"`
}

func NormalizeProviderInput(input ProviderAccountInput) (ProviderAccountInput, error) {
	input.Slug = strings.TrimSpace(input.Slug)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.Upstream = strings.TrimSpace(input.Upstream)
	input.AuthHeader = http.CanonicalHeaderKey(strings.TrimSpace(input.AuthHeader))
	input.Secret = strings.TrimSpace(input.Secret)
	if err := validateSlug(input.Slug); err != nil {
		return ProviderAccountInput{}, err
	}
	if input.DisplayName == "" {
		return ProviderAccountInput{}, fmt.Errorf("%w: display name is required", ErrInvalidConfig)
	}
	if err := validateProtocol(input.Protocol); err != nil {
		return ProviderAccountInput{}, err
	}
	upstream, err := normalizeUpstream(input.Upstream)
	if err != nil {
		return ProviderAccountInput{}, err
	}
	if err := validateAuthHeader(input.AuthHeader); err != nil {
		return ProviderAccountInput{}, err
	}
	input.Upstream = upstream
	seenModels := make(map[string]struct{}, len(input.Models))
	for i := range input.Models {
		model := &input.Models[i]
		model.ModelID = strings.TrimSpace(model.ModelID)
		model.DisplayName = strings.TrimSpace(model.DisplayName)
		if model.ModelID == "" || strings.ContainsAny(model.ModelID, " \t\r\n") {
			return ProviderAccountInput{}, fmt.Errorf("%w: provider model is invalid", ErrInvalidConfig)
		}
		if _, exists := seenModels[model.ModelID]; exists {
			return ProviderAccountInput{}, fmt.Errorf("%w: duplicate provider model %q", ErrInvalidConfig, model.ModelID)
		}
		seenModels[model.ModelID] = struct{}{}
	}
	return input, nil
}

func NormalizeGatewayInput(input GatewayInput) (GatewayInput, error) {
	input.Slug = strings.TrimSpace(input.Slug)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	if err := validateSlug(input.Slug); err != nil {
		return GatewayInput{}, err
	}
	if input.DisplayName == "" {
		return GatewayInput{}, fmt.Errorf("%w: display name is required", ErrInvalidConfig)
	}
	if err := validateProtocol(input.Protocol); err != nil {
		return GatewayInput{}, err
	}
	if len(input.Routes) == 0 {
		return GatewayInput{}, fmt.Errorf("%w: at least one model route is required", ErrInvalidConfig)
	}
	seenPublicModels := make(map[string]struct{}, len(input.Routes))
	for i := range input.Routes {
		route := &input.Routes[i]
		route.PublicModel = strings.TrimSpace(route.PublicModel)
		route.ProviderModel = strings.TrimSpace(route.ProviderModel)
		if route.PublicModel == "" || strings.ContainsAny(route.PublicModel, " \t\r\n") {
			return GatewayInput{}, fmt.Errorf("%w: route public model is invalid", ErrInvalidConfig)
		}
		if route.ProviderModel == "" || strings.ContainsAny(route.ProviderModel, " \t\r\n") {
			return GatewayInput{}, fmt.Errorf("%w: route provider model is invalid", ErrInvalidConfig)
		}
		if route.ProviderAccountID <= 0 {
			return GatewayInput{}, fmt.Errorf("%w: route provider account is required", ErrInvalidConfig)
		}
		if _, exists := seenPublicModels[route.PublicModel]; exists {
			return GatewayInput{}, fmt.Errorf("%w: duplicate public model %q", ErrInvalidConfig, route.PublicModel)
		}
		seenPublicModels[route.PublicModel] = struct{}{}
	}
	return input, nil
}

func NormalizeIssuedKeyInput(input IssuedKeyInput) (IssuedKeyInput, error) {
	input.Name = strings.TrimSpace(input.Name)
	if input.GatewayID <= 0 {
		return IssuedKeyInput{}, fmt.Errorf("%w: gateway is required", ErrInvalidConfig)
	}
	if input.Name == "" {
		return IssuedKeyInput{}, fmt.Errorf("%w: key name is required", ErrInvalidConfig)
	}
	if input.ExpiresAt != nil && !input.ExpiresAt.After(time.Now().UTC()) {
		return IssuedKeyInput{}, fmt.Errorf("%w: key expiration must be in the future", ErrInvalidConfig)
	}
	return input, nil
}

func (account ProviderAccount) HasModel(modelID string) bool {
	for _, model := range account.Models {
		if model.ModelID == modelID {
			return true
		}
	}
	return false
}

func (account ProviderAccount) HasEnabledModel(modelID string) bool {
	for _, model := range account.Models {
		if model.ModelID == modelID {
			return model.Enabled
		}
	}
	return false
}

func ProviderResponse(account ProviderAccount) ProviderAccountResponse {
	return ProviderAccountResponse{
		ID:          account.ID,
		Slug:        account.Slug,
		DisplayName: account.DisplayName,
		Enabled:     account.Enabled,
		Protocol:    account.Protocol,
		Upstream:    account.Upstream,
		AuthHeader:  account.AuthHeader,
		HasSecret:   account.Secret != "",
		Models:      account.Models,
		CreatedAt:   account.CreatedAt,
		UpdatedAt:   account.UpdatedAt,
	}
}

func GenerateKey() (string, []byte, string, string, error) {
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		return "", nil, "", "", fmt.Errorf("generate aggregate gateway key: %w", err)
	}
	key := KeyPrefix + base64.RawURLEncoding.EncodeToString(random)
	hash := KeyHash(key)
	prefix := key
	if len(prefix) > 12 {
		prefix = prefix[:12]
	}
	lastFour := key
	if len(lastFour) > 4 {
		lastFour = lastFour[len(lastFour)-4:]
	}
	return key, hash[:], prefix, lastFour, nil
}

func KeyHash(key string) [32]byte {
	return sha256.Sum256([]byte(strings.TrimSpace(key)))
}

func validateSlug(slug string) error {
	if !slugPattern.MatchString(slug) || slug == "v1" || slug == "gateways" || slug == "_admin" {
		return fmt.Errorf("%w: invalid slug %q", ErrInvalidConfig, slug)
	}
	return nil
}

func validateProtocol(protocol profile.Protocol) error {
	if protocol != profile.ProtocolAnthropic && protocol != profile.ProtocolOpenAI {
		return fmt.Errorf("%w: unsupported protocol %q", ErrInvalidConfig, protocol)
	}
	return nil
}

func normalizeUpstream(raw string) (string, error) {
	parsed, err := url.ParseRequestURI(raw)
	if err != nil {
		return "", fmt.Errorf("%w: upstream URL: %v", ErrInvalidConfig, err)
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", fmt.Errorf("%w: unsafe upstream URL", ErrInvalidConfig)
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func validateAuthHeader(header string) error {
	if header == "" || !headerPattern.MatchString(header) {
		return fmt.Errorf("%w: invalid auth header", ErrInvalidConfig)
	}
	switch http.CanonicalHeaderKey(header) {
	case "Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
		"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
		"Cookie", "Set-Cookie":
		return fmt.Errorf("%w: unsafe auth header", ErrInvalidConfig)
	default:
		return nil
	}
}
