package aggregate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Euphie/llm-proxy/internal/stats"
)

type Router struct {
	*Forwarder
	stats *stats.DB
}

type Forwarder struct {
	registry *Registry
	client   *http.Client
	store    *Store
	now      func() time.Time
}

func NewRouter(registry *Registry, client *http.Client, store *Store, sdb *stats.DB) http.Handler {
	return &Router{Forwarder: NewForwarder(registry, client, store), stats: sdb}
}

func NewForwarder(registry *Registry, client *http.Client, store *Store) *Forwarder {
	return &Forwarder{
		registry: registry,
		client:   client,
		store:    store,
		now:      time.Now,
	}
}

func (r *Router) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	slug, requestURI, ok := parseGatewayRequest(request)
	if !ok {
		writeGatewayError(w, http.StatusNotFound, "aggregate_gateway_not_found", "Aggregate gateway not found.")
		return
	}
	current := r.registry.Current()
	gateway := current.bySlug[slug]
	if gateway == nil {
		writeGatewayError(w, http.StatusNotFound, "aggregate_gateway_not_found", "Aggregate gateway not found.")
		return
	}
	if !gateway.Enabled {
		writeGatewayError(w, http.StatusServiceUnavailable, "aggregate_gateway_disabled", "Aggregate gateway disabled.")
		return
	}
	key, err := authenticateGatewayKey(current, gateway.ID, request.Header, r.now())
	if err != nil {
		writeGatewayError(w, http.StatusUnauthorized, "invalid_gateway_key", "Gateway key is invalid or expired.")
		return
	}

	body, err := io.ReadAll(request.Body)
	if err != nil {
		writeGatewayError(w, http.StatusInternalServerError, "request_read_failed", "Failed to read request body.")
		return
	}
	request.Body.Close()
	response, err := r.forward(request.Context(), gateway, request.Method, requestURI, request.Header, body)
	if err != nil {
		if request.Context().Err() != nil {
			return
		}
		slog.Error("aggregate upstream failed", "gateway", gateway.Slug, "err", err)
		writeGatewayError(w, http.StatusBadGateway, "upstream_error", "Aggregate upstream request failed.")
		return
	}
	if r.store != nil {
		if err := r.store.RecordKeyUse(contextWithoutCancel(request), key.ID, r.now()); err != nil {
			slog.Warn("record aggregate gateway key use failed", "key_id", key.ID, "err", err)
		}
	}
	captured := stream(w, response)
	if r.stats != nil && response.StatusCode < http.StatusBadRequest {
		r.stats.RecordAsync(stats.RequestMeta{
			GatewayID:         gateway.ID,
			GatewaySlug:       gateway.Slug,
			IssuedKeyID:       key.ID,
			IssuedKeyName:     key.Name,
			IssuedKeyPrefix:   key.Prefix,
			IssuedKeyLastFour: key.LastFour,
			Protocol:          gateway.Protocol,
			Kind:              "main",
			Path:              request.URL.Path,
		}, captured, stats.NewParser(gateway.Protocol))
	}
}

func (f *Forwarder) Forward(
	ctx context.Context,
	gatewayID int64,
	method string,
	requestURI string,
	headers http.Header,
	body []byte,
) (*http.Response, error) {
	current := f.registry.Current()
	gateway := current.byID[gatewayID]
	if gateway == nil {
		return gatewayErrorResponse(http.StatusNotFound, "aggregate_gateway_not_found", "Aggregate gateway not found."), nil
	}
	if !gateway.Enabled {
		return gatewayErrorResponse(http.StatusServiceUnavailable, "aggregate_gateway_disabled", "Aggregate gateway disabled."), nil
	}
	return f.forward(ctx, gateway, method, requestURI, headers, body)
}

func (f *Forwarder) forward(
	ctx context.Context,
	gateway *runtimeGateway,
	method string,
	requestURI string,
	headers http.Header,
	body []byte,
) (*http.Response, error) {
	publicModel, body, err := rewriteRequestModel(headers, body, gateway)
	if err != nil {
		return gatewayErrorResponse(http.StatusBadRequest, "invalid_model_request", err.Error()), nil
	}
	route, ok := gateway.selectRoute(publicModel)
	if !ok {
		return gatewayErrorResponse(http.StatusUnprocessableEntity, "model_route_not_found", "No aggregate model route matches the requested model."), nil
	}
	target := targetURL(route.Provider.Upstream, requestURI)
	return f.do(ctx, method, target, headers, route.Provider, body)
}

func parseGatewayRequest(request *http.Request) (slug, requestURI string, ok bool) {
	raw := request.RequestURI
	if raw == "" {
		raw = request.URL.RequestURI()
	}
	pathPart, query, hasQuery := strings.Cut(raw, "?")
	if !strings.HasPrefix(pathPart, "/gateways/") {
		return "", "", false
	}
	rest := strings.TrimPrefix(pathPart, "/gateways/")
	escapedSlug, escapedRemainder, found := strings.Cut(rest, "/")
	if !found || escapedSlug == "" {
		return "", "", false
	}
	decodedSlug, err := url.PathUnescape(escapedSlug)
	if err != nil || strings.Contains(decodedSlug, "/") {
		return "", "", false
	}
	requestURI = "/" + escapedRemainder
	if hasQuery {
		requestURI += "?" + query
	}
	return decodedSlug, requestURI, true
}

func authenticateGatewayKey(snapshot *runtimeSnapshot, gatewayID int64, headers http.Header, now time.Time) (runtimeKey, error) {
	token := gatewayToken(headers)
	if token == "" {
		return runtimeKey{}, ErrInvalidKey
	}
	hash := KeyHash(token)
	key, ok := snapshot.keys[hash]
	if !ok || key.GatewayID != gatewayID || !key.Enabled {
		return runtimeKey{}, ErrInvalidKey
	}
	if key.ExpiresAt != nil && !key.ExpiresAt.After(now.UTC()) {
		return runtimeKey{}, ErrInvalidKey
	}
	return key, nil
}

func gatewayToken(headers http.Header) string {
	if authorization := strings.TrimSpace(headers.Get("Authorization")); authorization != "" {
		scheme, token, ok := strings.Cut(authorization, " ")
		if ok && strings.EqualFold(scheme, "Bearer") {
			return strings.TrimSpace(token)
		}
	}
	if key := strings.TrimSpace(headers.Get("X-Api-Key")); key != "" {
		return key
	}
	return ""
}

func rewriteRequestModel(headers http.Header, body []byte, gateway *runtimeGateway) (string, []byte, error) {
	mediaType, _, err := mime.ParseMediaType(headers.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return "", nil, fmt.Errorf("request body must be application/json")
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", nil, fmt.Errorf("request body must be valid JSON")
	}
	var model string
	if raw := payload["model"]; len(raw) != 0 {
		_ = json.Unmarshal(raw, &model)
	}
	if strings.TrimSpace(model) == "" || strings.TrimSpace(model) != model {
		return "", nil, fmt.Errorf("request model is required")
	}
	route, ok := gateway.selectRoute(model)
	if !ok {
		return model, body, nil
	}
	if route.ProviderModel == model {
		return model, body, nil
	}
	encodedModel, err := json.Marshal(route.ProviderModel)
	if err != nil {
		return "", nil, err
	}
	payload["model"] = encodedModel
	rewritten, err := json.Marshal(payload)
	if err != nil {
		return "", nil, err
	}
	return model, rewritten, nil
}

func (g *runtimeGateway) selectRoute(publicModel string) (runtimeRoute, bool) {
	route, ok := g.Routes[publicModel]
	return route, ok
}

func (f *Forwarder) do(
	ctx context.Context,
	method string,
	target string,
	headers http.Header,
	provider *runtimeProvider,
	body []byte,
) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	copyHeaders(request.Header, headers)
	request.Header.Del("Authorization")
	request.Header.Del("X-Api-Key")
	request.Header.Del(provider.AuthHeader)
	request.Header.Set(provider.AuthHeader, providerAuthValue(provider.AuthHeader, provider.Secret))
	return f.client.Do(request)
}

func providerAuthValue(header, secret string) string {
	if !strings.EqualFold(header, "Authorization") || strings.ContainsAny(secret, " \t") {
		return secret
	}
	return "Bearer " + secret
}

func targetURL(upstream, requestURI string) string {
	return strings.TrimRight(upstream, "/") + "/" + strings.TrimLeft(requestURI, "/")
}

func stream(w http.ResponseWriter, resp *http.Response) []byte {
	defer resp.Body.Close()
	var captured bytes.Buffer
	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	flusher, canFlush := w.(http.Flusher)
	buffer := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buffer)
		if n > 0 {
			_, _ = captured.Write(buffer[:n])
			_, _ = w.Write(buffer[:n])
			if canFlush {
				flusher.Flush()
			}
		}
		if err != nil {
			return captured.Bytes()
		}
	}
}

func copyHeaders(dst, src http.Header) {
	connectionHeaders := make(map[string]struct{})
	for _, value := range src.Values("Connection") {
		for token := range strings.SplitSeq(value, ",") {
			if name := strings.TrimSpace(token); name != "" {
				connectionHeaders[http.CanonicalHeaderKey(name)] = struct{}{}
			}
		}
	}
	for k, vs := range src {
		if isHopByHopHeader(k) {
			continue
		}
		if _, remove := connectionHeaders[http.CanonicalHeaderKey(k)]; remove {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

func isHopByHopHeader(name string) bool {
	switch http.CanonicalHeaderKey(name) {
	case "Connection",
		"Proxy-Connection",
		"Keep-Alive",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Te",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade":
		return true
	default:
		return false
	}
}

func writeGatewayError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(gatewayErrorPayload(code, message))
}

func gatewayErrorResponse(status int, code, message string) *http.Response {
	var body bytes.Buffer
	_ = json.NewEncoder(&body).Encode(gatewayErrorPayload(code, message))
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
		Body: io.NopCloser(&body),
	}
}

func gatewayErrorPayload(code, message string) struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
} {
	return struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{
		Error: struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}{Code: code, Message: message},
	}
}

func contextWithoutCancel(request *http.Request) context.Context {
	return context.WithoutCancel(request.Context())
}
