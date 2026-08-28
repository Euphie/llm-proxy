package app

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Break caught: treating an aggregate gateway as a Profile passthrough instead of
// authenticating the issued gateway key and injecting the selected provider key.
func TestAggregateGatewayForwardsWithIssuedKeyAndProviderCredential(t *testing.T) {
	type upstreamObservation struct {
		path          string
		authorization string
		body          string
	}
	observed := make(chan upstreamObservation, 1)
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		body, _ := io.ReadAll(r.Body)
		observed <- upstreamObservation{
			path:          r.URL.RequestURI(),
			authorization: r.Header.Get("Authorization"),
			body:          string(body),
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-test","model":"provider-gpt-4o","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":34}}`)
	}))
	t.Cleanup(upstream.Close)

	application, err := New(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := application.Close(); err != nil {
			t.Errorf("close application: %v", err)
		}
	})
	cookies, csrf := authenticateAppAdmin(t, application)

	provider := request(application, http.MethodPost, "/_admin/api/provider-accounts",
		`{
			"slug":"openai-primary",
			"display_name":"OpenAI Primary",
			"enabled":true,
			"protocol":"openai",
			"upstream":`+quoteJSON(upstream.URL)+`,
			"auth_header":"Authorization",
			"secret":"upstream-secret",
			"models":[{
				"model_id":"provider-gpt-4o",
				"display_name":"Provider GPT-4o",
				"enabled":true
			}]
		}`, cookies, csrf)
	if provider.Code != http.StatusCreated {
		t.Fatalf("provider status=%d body=%s", provider.Code, provider.Body.String())
	}
	assertDoesNotContain(t, provider.Body.String(), "upstream-secret")
	var providerBody struct {
		ID        int64  `json:"id"`
		Slug      string `json:"slug"`
		HasSecret bool   `json:"has_secret"`
	}
	if err := json.Unmarshal(provider.Body.Bytes(), &providerBody); err != nil {
		t.Fatal(err)
	}
	if providerBody.ID == 0 || providerBody.Slug != "openai-primary" || !providerBody.HasSecret {
		t.Fatalf("provider body=%+v", providerBody)
	}

	gateway := request(application, http.MethodPost, "/_admin/api/aggregate-gateways",
		`{
			"slug":"team",
			"display_name":"Team Gateway",
			"enabled":true,
			"protocol":"openai",
			"routes":[{
				"public_model":"gpt-4o",
				"provider_account_id":`+intJSON(providerBody.ID)+`,
				"provider_model":"provider-gpt-4o",
				"enabled":true
			}]
		}`, cookies, csrf)
	if gateway.Code != http.StatusCreated {
		t.Fatalf("gateway status=%d body=%s", gateway.Code, gateway.Body.String())
	}
	var gatewayBody struct {
		ID     int64 `json:"id"`
		Routes []struct {
			PublicModel   string `json:"public_model"`
			ProviderModel string `json:"provider_model"`
		} `json:"routes"`
	}
	if err := json.Unmarshal(gateway.Body.Bytes(), &gatewayBody); err != nil {
		t.Fatal(err)
	}
	if gatewayBody.ID == 0 || len(gatewayBody.Routes) != 1 ||
		gatewayBody.Routes[0].PublicModel != "gpt-4o" ||
		gatewayBody.Routes[0].ProviderModel != "provider-gpt-4o" {
		t.Fatalf("gateway body=%+v", gatewayBody)
	}

	issued := request(application, http.MethodPost,
		"/_admin/api/aggregate-gateways/"+intJSON(gatewayBody.ID)+"/keys",
		`{"name":"local-dev","enabled":true}`, cookies, csrf)
	if issued.Code != http.StatusCreated {
		t.Fatalf("key status=%d body=%s", issued.Code, issued.Body.String())
	}
	var issuedBody struct {
		ID     int64  `json:"id"`
		Key    string `json:"key"`
		Prefix string `json:"prefix"`
	}
	if err := json.Unmarshal(issued.Body.Bytes(), &issuedBody); err != nil {
		t.Fatal(err)
	}
	if issuedBody.ID == 0 || !strings.HasPrefix(issuedBody.Key, "lgp_") ||
		issuedBody.Prefix == "" || !strings.HasPrefix(issuedBody.Key, issuedBody.Prefix) {
		t.Fatalf("issued key body=%+v", issuedBody)
	}

	proxyReq := httptest.NewRequest(http.MethodPost, "/gateways/team/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	proxyReq.Header.Set("Content-Type", "application/json")
	proxyReq.Header.Set("Authorization", "Bearer "+issuedBody.Key)
	proxyRes := httptest.NewRecorder()
	application.Handler().ServeHTTP(proxyRes, proxyReq)
	if proxyRes.Code != http.StatusOK ||
		!strings.Contains(proxyRes.Body.String(), `"model":"provider-gpt-4o"`) {
		t.Fatalf("proxy status=%d body=%s", proxyRes.Code, proxyRes.Body.String())
	}
	got := <-observed
	if got.path != "/v1/chat/completions" ||
		got.authorization != "Bearer upstream-secret" ||
		!strings.Contains(got.body, `"model":"provider-gpt-4o"`) ||
		strings.Contains(got.body, `"model":"gpt-4o"`) {
		t.Fatalf("upstream observed=%+v", got)
	}

	rejectedReq := httptest.NewRequest(http.MethodPost, "/gateways/team/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[]}`))
	rejectedReq.Header.Set("Content-Type", "application/json")
	rejectedReq.Header.Set("Authorization", "Bearer invalid")
	rejectedRes := httptest.NewRecorder()
	application.Handler().ServeHTTP(rejectedRes, rejectedReq)
	if rejectedRes.Code != http.StatusUnauthorized ||
		!strings.Contains(rejectedRes.Body.String(), "invalid_gateway_key") {
		t.Fatalf("rejected status=%d body=%s", rejectedRes.Code, rejectedRes.Body.String())
	}
	if upstreamCalls.Load() != 1 {
		t.Fatalf("upstream calls=%d, want 1", upstreamCalls.Load())
	}

	deadline := time.Now().Add(time.Second)
	for {
		var gatewayID, keyID sql.NullInt64
		var gatewaySlug, keyName, prefix string
		var inputTokens, outputTokens int
		err := application.db.QueryRow(`
			SELECT gateway_id, gateway_slug, issued_key_id, issued_key_name,
			       issued_key_prefix, input_tokens, output_tokens
			FROM usage
			WHERE gateway_id = ? AND issued_key_id = ?
		`, gatewayBody.ID, issuedBody.ID).Scan(
			&gatewayID, &gatewaySlug, &keyID, &keyName, &prefix, &inputTokens, &outputTokens,
		)
		if err == nil {
			if gatewaySlug != "team" || keyName != "local-dev" || prefix != issuedBody.Prefix ||
				inputTokens != 12 || outputTokens != 34 {
				t.Fatalf(
					"usage gateway=%v/%q key=%v/%q/%q tokens=%d/%d",
					gatewayID, gatewaySlug, keyID, keyName, prefix, inputTokens, outputTokens,
				)
			}
			break
		}
		if err != sql.ErrNoRows {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("aggregate gateway usage was not recorded")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Break caught: forcing Profiles to loop through the public Gateway Key path
// instead of resolving a configured aggregate gateway as an internal upstream.
func TestProfileCanUseAggregateGatewayAsInternalUpstream(t *testing.T) {
	type upstreamObservation struct {
		path          string
		authorization string
		callerKey     string
		body          string
	}
	observed := make(chan upstreamObservation, 1)
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		body, _ := io.ReadAll(r.Body)
		observed <- upstreamObservation{
			path:          r.URL.RequestURI(),
			authorization: r.Header.Get("Authorization"),
			callerKey:     r.Header.Get("X-Api-Key"),
			body:          string(body),
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-profile","model":"provider-gpt-4o","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":9}}`)
	}))
	t.Cleanup(upstream.Close)

	application, err := New(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := application.Close(); err != nil {
			t.Errorf("close application: %v", err)
		}
	})
	cookies, csrf := authenticateAppAdmin(t, application)

	provider := request(application, http.MethodPost, "/_admin/api/provider-accounts",
		`{
			"slug":"openai-profile-upstream",
			"display_name":"OpenAI Profile Upstream",
			"enabled":true,
				"protocol":"openai",
				"upstream":`+quoteJSON(upstream.URL)+`,
				"auth_header":"Authorization",
				"secret":"Bearer upstream-secret",
				"models":[{
					"model_id":"provider-gpt-4o",
					"display_name":"Provider GPT-4o",
					"enabled":true
				}]
			}`, cookies, csrf)
	if provider.Code != http.StatusCreated {
		t.Fatalf("provider status=%d body=%s", provider.Code, provider.Body.String())
	}
	var providerBody struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(provider.Body.Bytes(), &providerBody); err != nil {
		t.Fatal(err)
	}

	gateway := request(application, http.MethodPost, "/_admin/api/aggregate-gateways",
		`{
			"slug":"team-internal",
			"display_name":"Team Internal Gateway",
			"enabled":true,
			"protocol":"openai",
			"routes":[{
				"public_model":"gpt-4o",
				"provider_account_id":`+intJSON(providerBody.ID)+`,
				"provider_model":"provider-gpt-4o",
				"enabled":true
			}]
		}`, cookies, csrf)
	if gateway.Code != http.StatusCreated {
		t.Fatalf("gateway status=%d body=%s", gateway.Code, gateway.Body.String())
	}
	var gatewayBody struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(gateway.Body.Bytes(), &gatewayBody); err != nil {
		t.Fatal(err)
	}

	profile := request(application, http.MethodPost, "/_admin/api/profiles",
		`{
			"slug":"coding",
			"display_name":"Coding",
			"enabled":true,
			"make_default":true,
			"config":{
				"version":1,
				"protocol":"openai",
				"upstream_type":"aggregate_gateway",
				"upstream_gateway_id":`+intJSON(gatewayBody.ID)+`,
				"upstream":"",
				"vision":{
					"enabled":false,
					"transport":"openai_chat_completions",
					"model":"gpt-4o-mini",
					"unlisted_model_policy":"bypass",
					"max_tokens":2048,
					"timeout":"2m",
					"max_concurrency":4,
					"cache_ttl":"30m",
					"cache_max_entries":512
				},
				"overload_rules":[]
			}
		}`, cookies, csrf)
	if profile.Code != http.StatusCreated {
		t.Fatalf("profile status=%d body=%s", profile.Code, profile.Body.String())
	}

	proxyReq := httptest.NewRequest(http.MethodPost, "/coding/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	proxyReq.Header.Set("Content-Type", "application/json")
	proxyReq.Header.Set("Authorization", "Bearer caller-profile-key")
	proxyReq.Header.Set("X-Api-Key", "caller-profile-key")
	proxyRes := httptest.NewRecorder()
	application.Handler().ServeHTTP(proxyRes, proxyReq)
	if proxyRes.Code != http.StatusOK ||
		!strings.Contains(proxyRes.Body.String(), `"model":"provider-gpt-4o"`) {
		t.Fatalf("proxy status=%d body=%s", proxyRes.Code, proxyRes.Body.String())
	}

	got := <-observed
	if got.path != "/v1/chat/completions" ||
		got.authorization != "Bearer upstream-secret" ||
		got.callerKey != "" ||
		!strings.Contains(got.body, `"model":"provider-gpt-4o"`) ||
		strings.Contains(got.body, `"model":"gpt-4o"`) {
		t.Fatalf("upstream observed=%+v", got)
	}
	if upstreamCalls.Load() != 1 {
		t.Fatalf("upstream calls=%d, want 1", upstreamCalls.Load())
	}

	deadline := time.Now().Add(time.Second)
	for {
		var gatewayID, keyID sql.NullInt64
		var gatewaySlug string
		var inputTokens, outputTokens int
		err := application.db.QueryRow(`
			SELECT gateway_id, gateway_slug, issued_key_id, input_tokens, output_tokens
			FROM usage
			WHERE profile_slug = 'coding'
		`).Scan(&gatewayID, &gatewaySlug, &keyID, &inputTokens, &outputTokens)
		if err == nil {
			if !gatewayID.Valid || gatewayID.Int64 != gatewayBody.ID || gatewaySlug != "team-internal" ||
				keyID.Valid || inputTokens != 7 || outputTokens != 9 {
				t.Fatalf(
					"usage gateway=%v/%q key=%v tokens=%d/%d",
					gatewayID, gatewaySlug, keyID, inputTokens, outputTokens,
				)
			}
			break
		}
		if err != sql.ErrNoRows {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("internal aggregate Profile usage was not recorded")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestProfileVisionPreprocessingUsesAggregateGatewayRoutes(t *testing.T) {
	type upstreamObservation struct {
		model  string
		apiKey string
		body   string
	}
	observed := make(chan upstreamObservation, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			Model string `json:"model"`
		}
		_ = json.Unmarshal(body, &payload)
		observed <- upstreamObservation{
			model:  payload.Model,
			apiKey: r.Header.Get("X-Api-Key"),
			body:   string(body),
		}
		w.Header().Set("Content-Type", "application/json")
		if payload.Model == "provider-vision" {
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"A blue square"}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"id":"msg-main","model":"provider-text","content":[{"type":"text","text":"ok"}]}`)
	}))
	t.Cleanup(upstream.Close)

	application, err := New(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := application.Close(); err != nil {
			t.Errorf("close application: %v", err)
		}
	})
	cookies, csrf := authenticateAppAdmin(t, application)

	provider := request(application, http.MethodPost, "/_admin/api/provider-accounts",
		`{
			"slug":"anthropic-vision-upstream",
			"display_name":"Anthropic Vision Upstream",
			"enabled":true,
			"protocol":"anthropic",
			"upstream":`+quoteJSON(upstream.URL)+`,
			"auth_header":"X-Api-Key",
			"secret":"provider-secret",
			"models":[
				{"model_id":"provider-text","display_name":"Provider Text","enabled":true},
				{"model_id":"provider-vision","display_name":"Provider Vision","enabled":true}
			]
		}`, cookies, csrf)
	if provider.Code != http.StatusCreated {
		t.Fatalf("provider status=%d body=%s", provider.Code, provider.Body.String())
	}
	var providerBody struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(provider.Body.Bytes(), &providerBody); err != nil {
		t.Fatal(err)
	}

	gateway := request(application, http.MethodPost, "/_admin/api/aggregate-gateways",
		`{
			"slug":"vision-internal",
			"display_name":"Vision Internal Gateway",
			"enabled":true,
			"protocol":"anthropic",
			"routes":[
				{"public_model":"text-public","provider_account_id":`+intJSON(providerBody.ID)+`,"provider_model":"provider-text","enabled":true},
				{"public_model":"vision-public","provider_account_id":`+intJSON(providerBody.ID)+`,"provider_model":"provider-vision","enabled":true}
			]
		}`, cookies, csrf)
	if gateway.Code != http.StatusCreated {
		t.Fatalf("gateway status=%d body=%s", gateway.Code, gateway.Body.String())
	}
	var gatewayBody struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(gateway.Body.Bytes(), &gatewayBody); err != nil {
		t.Fatal(err)
	}

	profileResponse := request(application, http.MethodPost, "/_admin/api/profiles",
		`{
			"slug":"vision-profile",
			"display_name":"Vision Profile",
			"enabled":true,
			"make_default":true,
			"config":{
				"version":1,
				"protocol":"anthropic",
				"upstream_type":"aggregate_gateway",
				"upstream_gateway_id":`+intJSON(gatewayBody.ID)+`,
				"upstream":"",
				"models":[
					{"id":"text-public","supports_vision":false},
					{"id":"vision-public","supports_vision":true}
				],
				"vision":{
					"enabled":true,
					"transport":"anthropic_messages",
					"model":"vision-public",
					"unlisted_model_policy":"bypass",
					"max_tokens":2048,
					"timeout":"2m",
					"max_concurrency":4,
					"cache_ttl":"30m",
					"cache_max_entries":512
				},
				"overload_rules":[]
			}
		}`, cookies, csrf)
	if profileResponse.Code != http.StatusCreated {
		t.Fatalf("profile status=%d body=%s", profileResponse.Code, profileResponse.Body.String())
	}

	proxyReq := httptest.NewRequest(http.MethodPost, "/vision-profile/v1/messages", strings.NewReader(`{
		"model":"text-public",
		"max_tokens":256,
		"messages":[{"role":"user","content":[
			{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}},
			{"type":"text","text":"What is shown?"}
		]}]
	}`))
	proxyReq.Header.Set("Content-Type", "application/json")
	proxyReq.Header.Set("X-Api-Key", "caller-key")
	proxyRes := httptest.NewRecorder()
	application.Handler().ServeHTTP(proxyRes, proxyReq)
	if proxyRes.Code != http.StatusOK {
		t.Fatalf("proxy status=%d body=%s", proxyRes.Code, proxyRes.Body.String())
	}

	requests := map[string]upstreamObservation{}
	for range 2 {
		got := <-observed
		requests[got.model] = got
	}
	visionRequest := requests["provider-vision"]
	if visionRequest.apiKey != "provider-secret" || !strings.Contains(visionRequest.body, `"type":"image"`) {
		t.Fatalf("vision request=%+v", visionRequest)
	}
	mainRequest := requests["provider-text"]
	if mainRequest.apiKey != "provider-secret" ||
		strings.Contains(mainRequest.body, `"type":"image"`) ||
		!strings.Contains(mainRequest.body, "A blue square") {
		t.Fatalf("main request=%+v", mainRequest)
	}
}

func authenticateAppAdmin(t *testing.T, application *App) ([]*http.Cookie, string) {
	t.Helper()
	login := request(application, http.MethodPost, "/_admin/api/login",
		`{"username":"admin","password":"admin"}`, nil, "")
	if login.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", login.Code, login.Body.String())
	}
	loginCookies := login.Result().Cookies()
	loginCSRF := findCookie(t, loginCookies, "llm_proxy_csrf").Value
	password := request(application, http.MethodPost, "/_admin/api/password",
		`{"current_password":"admin","new_password":"strong-admin-password"}`,
		loginCookies, loginCSRF)
	if password.Code != http.StatusOK {
		t.Fatalf("password status=%d body=%s", password.Code, password.Body.String())
	}
	cookies := password.Result().Cookies()
	return cookies, findCookie(t, cookies, "llm_proxy_csrf").Value
}

func quoteJSON(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func intJSON(value int64) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func assertDoesNotContain(t *testing.T, haystack string, needles ...string) {
	t.Helper()
	for _, needle := range needles {
		if strings.Contains(haystack, needle) {
			t.Fatalf("response leaked %q: %s", needle, haystack)
		}
	}
}
