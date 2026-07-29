# Admin API and Authentication Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Expose a secure single-admin JSON control plane, connect Profile mutations to the hot runtime registry, and switch the production entrypoint from YAML to `DATA_DIR`.

**Architecture:** Add an `admin` package for password hashing, database-backed Sessions, CSRF, rate limiting, Profile orchestration, and JSON handlers. Add an `app` composition root that owns the shared database, usage store, Profile registry, admin API, and data router.

**Tech Stack:** Go 1.25, `net/http`, `database/sql`, `crypto/rand`, `crypto/sha256`, `golang.org/x/crypto/argon2`, SQLite, Docker.

## Global Constraints

- Complete `2026-07-29-profile-runtime-foundation.md` first.
- The only administrator username is `admin`.
- An empty database initializes password `admin` with `must_change_password=true`.
- The accepted public-first-login risk must remain prominently logged and returned by the admin status endpoint.
- Until the password changes and an enabled default Profile exists, data-plane requests return `503 proxy_not_configured`.
- All management URLs live under `/_admin`.
- Session plaintext and CSRF plaintext exist only in browser cookies; SQLite stores hashes.
- Profile and Session APIs never return password hashes, Session hashes, or CSRF hashes.
- API keys and Authorization headers are never accepted by management request models.
- State-changing authenticated requests require a valid CSRF header.
- Do not execute any `git commit` until the user explicitly authorizes commits; commit commands below are suggested checkpoints only.

---

### Task 1: Password hashing and singleton admin account

**Files:**
- Create: `internal/admin/password.go`
- Create: `internal/admin/password_test.go`
- Create: `internal/admin/account.go`
- Create: `internal/admin/account_test.go`
- Modify: `go.mod`

**Interfaces:**
- Consumes: `admin_account` and `admin_sessions` tables from the foundation plan.
- Produces: `HashPassword(password string) (string, error)`
- Produces: `VerifyPassword(encoded, password string) bool`
- Produces: `NewAccountStore(db *sql.DB) *AccountStore`
- Produces: `EnsureDefault`, `Get`, `Authenticate`, and `ChangePassword`.

- [ ] **Step 1: Write failing password tests**

```go
func TestHashPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if hash == "correct horse battery staple" {
		t.Fatal("password was stored in plaintext")
	}
	if !VerifyPassword(hash, "correct horse battery staple") {
		t.Fatal("correct password rejected")
	}
	if VerifyPassword(hash, "wrong password") {
		t.Fatal("wrong password accepted")
	}
}

func TestHashPasswordUsesFreshSalt(t *testing.T) {
	first, _ := HashPassword("same password")
	second, _ := HashPassword("same password")
	if first == second {
		t.Fatal("hashes reused a salt")
	}
}
```

- [ ] **Step 2: Run tests and verify failure**

Run:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  golang:1.25-bookworm \
  go test ./internal/admin -run TestHashPassword -v
```

Expected: FAIL because `internal/admin` does not exist.

- [ ] **Step 3: Implement Argon2id encoding**

Use:

```go
const (
	argonMemory      = 64 * 1024
	argonIterations  = 3
	argonParallelism = 2
	argonSaltLength  = 16
	argonKeyLength   = 32
)
```

Encode hashes as:

```text
$argon2id$v=19$m=65536,t=3,p=2$<base64-salt>$<base64-key>
```

`VerifyPassword` must reject malformed encodings without panicking and compare derived keys with `subtle.ConstantTimeCompare`.

- [ ] **Step 4: Write failing account lifecycle tests**

```go
func TestAccountStoreInitializesAndChangesDefaultPassword(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewAccountStore(db)
	ctx := context.Background()

	account, created, err := store.EnsureDefault(ctx)
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	if account.Username != "admin" || !account.MustChangePassword {
		t.Fatalf("account=%+v", account)
	}
	if _, err := store.Authenticate(ctx, "admin", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := store.ChangePassword(ctx, account.ID, "admin", "long-enough-password"); err != nil {
		t.Fatal(err)
	}
	updated, _ := store.Get(ctx)
	if updated.MustChangePassword || updated.AuthVersion != account.AuthVersion+1 {
		t.Fatalf("updated=%+v", updated)
	}
}
```

Also test wrong username, wrong current password, a new password shorter than 10 characters, idempotent `EnsureDefault`, and Session deletion during password change.

- [ ] **Step 5: Implement account storage**

Define:

```go
type Account struct {
	ID                 int64
	Username           string
	MustChangePassword bool
	AuthVersion        int64
	UpdatedAt          time.Time
}

func NewAccountStore(db *sql.DB) *AccountStore
func (s *AccountStore) EnsureDefault(ctx context.Context) (Account, bool, error)
func (s *AccountStore) Get(ctx context.Context) (Account, error)
func (s *AccountStore) Authenticate(ctx context.Context, username, password string) (Account, error)
func (s *AccountStore) ChangePassword(
	ctx context.Context,
	accountID int64,
	currentPassword, newPassword string,
) error
```

`EnsureDefault` inserts `admin` with a hash of `admin` only when the singleton row is absent. `ChangePassword` must update the hash, clear `must_change_password`, increment `auth_version`, and delete all Sessions in one transaction.

Expose `ErrInvalidCredentials` and `ErrWeakPassword`.

- [ ] **Step 6: Run admin account tests**

Run:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  golang:1.25-bookworm \
  go test ./internal/admin -run 'TestHashPassword|TestAccountStore' -v
```

Expected: PASS.

- [ ] **Step 7: Suggested commit checkpoint**

After explicit user approval:

```bash
git add go.mod go.sum internal/admin/password* internal/admin/account*
git commit -m "feat: add administrator password storage"
```

---

### Task 2: Database-backed Session and CSRF tokens

**Files:**
- Create: `internal/admin/session.go`
- Create: `internal/admin/session_test.go`

**Interfaces:**
- Consumes: `admin_sessions`.
- Produces: `NewSessionStore(db *sql.DB, now func() time.Time) *SessionStore`
- Produces: `Create`, `Authenticate`, `Revoke`, and `CleanupExpired`.

- [ ] **Step 1: Write failing Session tests**

```go
func TestSessionStoreKeepsOnlyTokenHashes(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	store := NewSessionStore(db, func() time.Time { return now })

	session, err := store.Create(context.Background(), 3, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var tokenHash, csrfHash []byte
	if err := db.QueryRow(`SELECT token_hash, csrf_hash FROM admin_sessions`).Scan(
		&tokenHash, &csrfHash,
	); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(tokenHash, []byte(session.Token)) ||
		bytes.Contains(csrfHash, []byte(session.CSRFToken)) {
		t.Fatal("plaintext token persisted")
	}

	got, err := store.Authenticate(context.Background(), session.Token, 3)
	if err != nil || got.ExpiresAt != now.Add(24*time.Hour) {
		t.Fatalf("session=%+v err=%v", got, err)
	}
	if err := store.VerifyCSRF(
		context.Background(), session.Token, session.CSRFToken,
	); err != nil {
		t.Fatal(err)
	}
}
```

Add tests for wrong Session token, wrong CSRF token, auth-version mismatch, expiration boundary, explicit revoke, and cleanup.

- [ ] **Step 2: Run focused test and verify failure**

Run:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  golang:1.25-bookworm \
  go test ./internal/admin -run TestSessionStore -v
```

Expected: FAIL because `SessionStore` does not exist.

- [ ] **Step 3: Implement token creation and hashing**

Define:

```go
const sessionTTL = 24 * time.Hour

type Session struct {
	Token      string
	CSRFToken  string
	AuthVersion int64
	ExpiresAt  time.Time
}

func NewSessionStore(db *sql.DB, now func() time.Time) *SessionStore
func (s *SessionStore) Create(ctx context.Context, authVersion int64, ttl time.Duration) (Session, error)
func (s *SessionStore) Authenticate(
	ctx context.Context,
	token string,
	authVersion int64,
) (Session, error)
func (s *SessionStore) VerifyCSRF(ctx context.Context, token, csrfToken string) error
func (s *SessionStore) Revoke(ctx context.Context, token string) error
func (s *SessionStore) CleanupExpired(ctx context.Context) error
```

Generate 32 random bytes for each token with `crypto/rand`, encode using `base64.RawURLEncoding`, and persist `sha256.Sum256` values. Compare hashes with `subtle.ConstantTimeCompare`.

- [ ] **Step 4: Run Session tests**

Run:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  golang:1.25-bookworm \
  go test ./internal/admin -run TestSessionStore -v
```

Expected: PASS.

- [ ] **Step 5: Suggested commit checkpoint**

After explicit user approval:

```bash
git add internal/admin/session.go internal/admin/session_test.go
git commit -m "feat: add database-backed admin Sessions"
```

---

### Task 3: Login limiting and authentication middleware

**Files:**
- Create: `internal/admin/limiter.go`
- Create: `internal/admin/limiter_test.go`
- Create: `internal/admin/auth.go`
- Create: `internal/admin/auth_test.go`

**Interfaces:**
- Consumes: `AccountStore` and `SessionStore`.
- Produces: `AuthService.Login`, `Logout`, and request authentication.
- Produces middleware used by every admin handler.

- [ ] **Step 1: Write failing rate-limit tests**

```go
func TestLoginLimiterAppliesAddressAndGlobalLimits(t *testing.T) {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	limiter := NewLoginLimiter(func() time.Time { return now })

	for i := 0; i < 5; i++ {
		if !limiter.Allow("192.0.2.1") {
			t.Fatalf("attempt %d unexpectedly denied", i+1)
		}
	}
	if limiter.Allow("192.0.2.1") {
		t.Fatal("sixth address attempt allowed")
	}
	for i := 0; i < 15; i++ {
		if !limiter.Allow(fmt.Sprintf("192.0.2.%d", i+2)) {
			t.Fatalf("global attempt %d unexpectedly denied", i+6)
		}
	}
	if limiter.Allow("198.51.100.1") {
		t.Fatal("twenty-first global attempt allowed")
	}
}
```

Advance `now` by one minute and assert both windows reset.

- [ ] **Step 2: Implement the fixed-window limiter**

Use exact limits:

```go
const (
	perAddressAttempts = 5
	globalAttempts     = 20
	loginWindow        = time.Minute
)
```

Key address limits from `net.SplitHostPort(r.RemoteAddr)`. Do not trust `X-Forwarded-For` in the first version.

- [ ] **Step 3: Write failing authentication middleware tests**

Test:

- login sets `llm_proxy_session` as `HttpOnly` and `SameSite=Strict`;
- login sets `llm_proxy_csrf` as non-HttpOnly and `SameSite=Strict`;
- HTTPS requests set both cookies `Secure`;
- a protected GET requires only Session authentication;
- POST, PUT, PATCH, and DELETE require `X-CSRF-Token`;
- a Session created before password change is rejected afterward;
- a must-change Session can access only Session status, password change, and logout.

Use this request shape:

```go
request := httptest.NewRequest(http.MethodPost, "/_admin/api/login",
	strings.NewReader(`{"username":"admin","password":"admin"}`))
request.RemoteAddr = "192.0.2.10:1234"
request.Header.Set("Content-Type", "application/json")
```

- [ ] **Step 4: Implement `AuthService`**

Define:

```go
type Principal struct {
	AccountID          int64
	Username           string
	AuthVersion        int64
	MustChangePassword bool
}

func NewAuthService(
	accounts *AccountStore,
	sessions *SessionStore,
	limiter *LoginLimiter,
) *AuthService

func (s *AuthService) Login(
	ctx context.Context,
	remoteAddr, username, password string,
) (Principal, Session, error)
func (s *AuthService) Logout(ctx context.Context, token string) error
func (s *AuthService) Authenticate(
	ctx context.Context,
	sessionToken, csrfToken string,
	requireCSRF bool,
) (Principal, error)
```

Expose `ErrRateLimited`, `ErrUnauthorized`, `ErrCSRF`, and `ErrPasswordChangeRequired`.

- [ ] **Step 5: Implement cookie and middleware helpers**

Use cookie paths `/_admin`, 24-hour maximum age, and:

```go
func requestRequiresCSRF(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}
```

Set `Secure` when `r.TLS != nil` or `X-Forwarded-Proto` equals `https`. The latter affects cookie delivery only; it does not grant authentication.

- [ ] **Step 6: Run auth tests**

Run:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  golang:1.25-bookworm \
  go test -race ./internal/admin -run 'TestLoginLimiter|TestAuth' -v
```

Expected: PASS with no race report.

- [ ] **Step 7: Suggested commit checkpoint**

After explicit user approval:

```bash
git add internal/admin/limiter* internal/admin/auth*
git commit -m "feat: protect admin endpoints with Sessions"
```

---

### Task 4: Profile application service

**Files:**
- Create: `internal/admin/profile_service.go`
- Create: `internal/admin/profile_service_test.go`
- Modify: `internal/profile/store.go`
- Modify: `internal/profile/store_test.go`

**Interfaces:**
- Consumes: `profile.Store` for reads and the process-wide
  `gateway.Coordinator` for every mutation.
- Produces: orchestration methods used by HTTP handlers.

- [ ] **Step 1: Write failing hot-save service tests**

```go
func TestProfileServicePublishesOnlyAfterDatabaseCommit(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
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
	coordinator := gateway.NewCoordinator(store, registry, builder)
	service := NewProfileService(store, coordinator)

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

	res := httptest.NewRecorder()
	gateway.NewRouter(registry).ServeHTTP(
		res, httptest.NewRequest(http.MethodPost, "/v1/messages", nil),
	)
	if res.Body.String() != "https://one.example" {
		t.Fatalf("runtime=%q", res.Body.String())
	}
}
```

Add tests proving duplicate-slug failure leaves the existing runtime unchanged, copying publishes the new slug, changing default switches root routing, and deleting removes the old slug.

- [ ] **Step 2: Run focused tests and verify failure**

Run:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  golang:1.25-bookworm \
  go test ./internal/admin -run TestProfileService -v
```

Expected: FAIL because `ProfileService` does not exist.

- [ ] **Step 3: Implement service methods**

Define:

```go
func NewProfileService(
	store *profile.Store,
	coordinator *gateway.Coordinator,
) *ProfileService

func (s *ProfileService) List(ctx context.Context) ([]profile.Record, int64, error)
func (s *ProfileService) Get(ctx context.Context, id int64) (profile.Record, error)
func (s *ProfileService) Save(
	ctx context.Context,
	input profile.SaveInput,
	makeDefault bool,
) (profile.Record, error)
func (s *ProfileService) Copy(
	ctx context.Context,
	id int64,
	slug, displayName string,
) (profile.Record, error)
func (s *ProfileService) SetDefault(ctx context.Context, id int64) error
func (s *ProfileService) Delete(
	ctx context.Context,
	id, replacementDefaultID int64,
) error
```

Call `Record.Resolve` before every write. Route every mutation through the
single process-wide `gateway.Coordinator`; do not mutate `profile.Store` or
`gateway.Registry` directly from the service. The Coordinator owns commit,
incremental publication, and full-resync error handling.

Replace the transitional English SQLite error-string match in
`profile.Store` with `errors.As` plus the modernc SQLite constraint code so a
duplicate slug remains a stable `profile.ErrSlugConflict` for the API's
`409` mapping.

- [ ] **Step 4: Run service tests**

Run:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  golang:1.25-bookworm \
  go test -race ./internal/admin -run TestProfileService -v
```

Expected: PASS.

- [ ] **Step 5: Suggested commit checkpoint**

After explicit user approval:

```bash
git add internal/admin/profile_service*
git commit -m "feat: hot-publish Profile changes"
```

---

### Task 5: JSON admin API

**Files:**
- Create: `internal/admin/api.go`
- Create: `internal/admin/errors.go`
- Create: `internal/admin/auth_handlers.go`
- Create: `internal/admin/profile_handlers.go`
- Create: `internal/admin/stats_handlers.go`
- Create: `internal/admin/system_handlers.go`
- Create: `internal/admin/api_test.go`

**Interfaces:**
- Consumes: `AuthService`, `ProfileService`, `stats.DB`, and database metadata.
- Produces: `admin.NewAPI(deps Dependencies) http.Handler`

- [ ] **Step 1: Write failing API contract tests**

Test these exact contracts:

```go
func TestLoginResponseAndProfileGate(t *testing.T) {
	api := newTestAPI(t)

	login := httptest.NewRequest(http.MethodPost, "/_admin/api/login",
		strings.NewReader(`{"username":"admin","password":"admin"}`))
	login.Header.Set("Content-Type", "application/json")
	login.RemoteAddr = "192.0.2.1:1234"
	loginRes := httptest.NewRecorder()
	api.ServeHTTP(loginRes, login)
	if loginRes.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", loginRes.Code, loginRes.Body.String())
	}

	var body struct {
		MustChangePassword bool `json:"must_change_password"`
	}
	if err := json.NewDecoder(loginRes.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.MustChangePassword {
		t.Fatal("initial login did not require password change")
	}
}
```

Add tests for invalid JSON, unknown fields, unauthorized access, CSRF rejection, password change, Profile CRUD, duplicate slug `409`, validation `422`, default switching, filtered stats, and System response redaction.
Also test that `GET /_admin/api/session` returns the authenticated username, `must_change_password`, and initialization state after a page refresh.
The Profile-list test must assert that each item contains a `usage_30d`
object populated from `stats.ProfileSummaries`.

- [ ] **Step 2: Run API tests and verify failure**

Run:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  golang:1.25-bookworm \
  go test ./internal/admin -run 'TestLoginResponse|TestAPI' -v
```

Expected: FAIL because the API handler does not exist.

- [ ] **Step 3: Define stable JSON helpers**

Use this error envelope everywhere:

```go
type errorResponse struct {
	Error struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Fields  map[string]string `json:"fields,omitempty"`
	} `json:"error"`
}
```

Decode JSON with `DisallowUnknownFields`, enforce `Content-Type: application/json` for write endpoints, limit request bodies to 1 MiB, and set `Content-Type: application/json; charset=utf-8`.

- [ ] **Step 4: Register exact routes**

Use a dedicated `http.ServeMux`:

```go
mux.HandleFunc("POST /_admin/api/login", api.login)
mux.HandleFunc("POST /_admin/api/logout", api.logout)
mux.HandleFunc("GET /_admin/api/session", api.getSession)
mux.HandleFunc("POST /_admin/api/password", api.changePassword)
mux.HandleFunc("GET /_admin/api/profiles", api.listProfiles)
mux.HandleFunc("POST /_admin/api/profiles", api.createProfile)
mux.HandleFunc("GET /_admin/api/profiles/{id}", api.getProfile)
mux.HandleFunc("PUT /_admin/api/profiles/{id}", api.updateProfile)
mux.HandleFunc("POST /_admin/api/profiles/{id}/copy", api.copyProfile)
mux.HandleFunc("DELETE /_admin/api/profiles/{id}", api.deleteProfile)
mux.HandleFunc("PUT /_admin/api/default-profile", api.setDefaultProfile)
mux.HandleFunc("GET /_admin/api/stats", api.getStats)
mux.HandleFunc("GET /_admin/api/system", api.getSystem)
```

Middleware must permit login without a Session, require a Session everywhere else, require CSRF on writes, and restrict must-change Sessions to session status, password change, and logout.

`GET /_admin/api/profiles` returns:

```json
{
  "default_profile_id": 1,
  "profiles": [
    {
      "id": 1,
      "slug": "coding",
      "display_name": "Coding",
      "enabled": true,
      "config": {},
      "usage_30d": {
        "requests": 12,
        "input_tokens": 100,
        "output_tokens": 50
      }
    }
  ]
}
```

Compute summaries with one aggregate query, not one query per Profile.

- [ ] **Step 5: Map domain failures explicitly**

Use:

| Domain error | HTTP status | code |
|---|---:|---|
| `admin.ErrInvalidCredentials` | 401 | `invalid_credentials` |
| `admin.ErrRateLimited` | 429 | `rate_limited` |
| `admin.ErrCSRF` | 403 | `csrf_failed` |
| `profile.ErrNotFound` | 404 | `profile_not_found` |
| `profile.ErrSlugConflict` | 409 | `profile_slug_conflict` |
| `profile.ErrInvalidConfig` | 422 | `validation_error` |
| `profile.ErrDefaultRequired` | 409 | `default_profile_required` |

Unexpected errors return `500 internal_error` and are logged without request bodies or headers.

After a successful password change, create a new Session using the incremented
`auth_version` and replace both cookies in the same response. This keeps the
current browser signed in while ensuring every pre-change Session is revoked.

- [ ] **Step 6: Add security headers**

Every admin response sets:

```text
Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'
X-Content-Type-Options: nosniff
Referrer-Policy: no-referrer
Cache-Control: no-store
```

- [ ] **Step 7: Run API tests**

Run:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  golang:1.25-bookworm \
  go test -race ./internal/admin -v
```

Expected: PASS.

- [ ] **Step 8: Suggested commit checkpoint**

After explicit user approval:

```bash
git add internal/admin
git commit -m "feat: expose Profile administration API"
```

---

### Task 6: Application composition and YAML removal

**Files:**
- Create: `internal/app/app.go`
- Create: `internal/app/app_test.go`
- Modify: `cmd/llm-proxy/main.go`
- Replace: `cmd/llm-proxy/main_test.go`
- Delete: `internal/config/config.go`
- Delete: `internal/config/config_test.go`
- Modify: `go.mod`
- Modify: `.env.example`
- Modify: `.gitignore`
- Create: `.dockerignore`
- Modify: `Dockerfile`
- Modify: `docker-compose.yml`
- Modify: `Makefile`

**Interfaces:**
- Consumes every prior task.
- Produces: `app.New(options Options) (*App, error)`
- Produces: `App.Handler() http.Handler` and `App.Close() error`.

- [ ] **Step 1: Write failing application startup tests**

```go
func TestNewStartsUnconfiguredAndServesAdminAPI(t *testing.T) {
	application, err := New(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()

	proxyReq := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	proxyRes := httptest.NewRecorder()
	application.Handler().ServeHTTP(proxyRes, proxyReq)
	if proxyRes.Code != http.StatusServiceUnavailable ||
		!strings.Contains(proxyRes.Body.String(), "proxy_not_configured") {
		t.Fatalf("proxy status=%d body=%q", proxyRes.Code, proxyRes.Body.String())
	}

	loginReq := httptest.NewRequest(http.MethodPost, "/_admin/api/login",
		strings.NewReader(`{"username":"admin","password":"admin"}`))
	loginReq.Header.Set("Content-Type", "application/json")
	loginReq.RemoteAddr = "192.0.2.1:1234"
	loginRes := httptest.NewRecorder()
	application.Handler().ServeHTTP(loginRes, loginReq)
	if loginRes.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%q", loginRes.Code, loginRes.Body.String())
	}
}
```

Also test reopening the same data directory preserves password and Profiles.

- [ ] **Step 2: Run app tests and verify failure**

Run:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  golang:1.25-bookworm \
  go test ./internal/app -v
```

Expected: FAIL because `internal/app` does not exist.

- [ ] **Step 3: Implement the composition root**

Define:

```go
type Options struct {
	DataDir string
}

type App struct {
	db      *sql.DB
	handler http.Handler
}

func New(options Options) (*App, error)
func (a *App) Handler() http.Handler
func (a *App) Close() error
```

`New` must:

1. default blank `DataDir` to `./data`;
2. open `database.Open`;
3. create `stats.DB`, account/session stores, Profile store, registry,
   process-wide Profile Coordinator, and shared HTTP client;
4. call `EnsureDefault`;
5. load persisted Profile records;
6. publish registry only when the password no longer requires change;
7. mount `/_admin/` before the data router.

The Profile builder resolves the record and calls `proxy.New(runtime, client, usageStore)`.

- [ ] **Step 4: Replace main configuration**

`cmd/llm-proxy/main.go` reads:

```go
listen := envOrDefault("LISTEN", ":8080")
dataDir := envOrDefault("DATA_DIR", "./data")
```

Support optional flags `-listen` and `-data-dir` with environment values as defaults. Remove `CONFIG_FILE`, YAML loading, stats Basic Auth, and direct construction of proxy handlers.

- [ ] **Step 5: Update Docker and Make targets**

Use:

```yaml
services:
  llm-proxy:
    ports:
      - "${HOST:-127.0.0.1}:${PORT:-8087}:8080"
    volumes:
      - type: bind
        source: ${DATA_DIR:-./data}
        target: /app/data
    environment:
      LISTEN: :8080
      DATA_DIR: /app/data
```

Remove `COPY config.yaml` and `CONFIG_FILE` from `Dockerfile`. Change `.env.example` to:

```env
HOST=127.0.0.1
PORT=8087
DATA_DIR=./data
```

Change `make run` to:

```make
run: build
	DATA_DIR=./data ./bin/$(BIN)
```

Create `.dockerignore` so local credentials and databases never enter the
Docker build context:

```text
.git
.env
bin
data
config-bak.yaml
config copy.yaml
.DS_Store
```

Rename the `.gitignore` comment above `data/` from `统计数据库` to `运行数据`;
keep `.env` and `config-bak.yaml` ignored.

- [ ] **Step 6: Remove runtime YAML code**

Delete `internal/config`. Remove `gopkg.in/yaml.v3` from `go.mod`, make `modernc.org/sqlite` a direct dependency, add `golang.org/x/crypto` as a direct dependency, and run:

```bash
docker run --rm \
  -v "$PWD":/src -w /src \
  golang:1.25-bookworm \
  go mod tidy
```

The container performs the mechanical module rewrite; inspect `go.mod` and `go.sum` afterward.
Also remove the transitional `stats.Open(path)` and `Close()` adapter; the
application now owns the single `*sql.DB` returned by `database.Open`.

- [ ] **Step 7: Run application and full Go tests**

Run:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  golang:1.25-bookworm \
  sh -c 'go test ./... && go test -race ./... && go vet ./...'
```

Expected: PASS.

- [ ] **Step 8: Suggested commit checkpoint**

After explicit user approval:

```bash
git add .dockerignore .env.example .gitignore Dockerfile Makefile docker-compose.yml go.mod go.sum cmd internal
git commit -m "feat: switch runtime configuration to Profile database"
```

---

### Task 7: Full API initialization integration test

**Files:**
- Create: `internal/app/initialization_test.go`

**Interfaces:**
- Exercises the complete `app.Handler` with real SQLite, cookies, CSRF, Profile API, registry, proxy, and usage recording.

- [ ] **Step 1: Write the end-to-end HTTP lifecycle test**

Create a test that:

1. starts an `httptest.Server` upstream returning an Anthropic usage response;
2. creates `app.New` with `t.TempDir`;
3. logs in using `admin/admin`;
4. captures Session and CSRF cookies;
5. verifies Profile creation is blocked before password change;
6. changes password to `strong-admin-password`;
7. creates an enabled Anthropic Profile with `make_default=true`;
8. sends `POST /v1/messages`;
9. asserts the upstream receives `/v1/messages` and the caller Authorization header;
10. polls the usage table until a `main` row exists for the new Profile.

Use a helper:

```go
func authenticatedRequest(
	t *testing.T,
	method, target string,
	body io.Reader,
	session, csrf *http.Cookie,
) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, target, body)
	req.AddCookie(session)
	req.AddCookie(csrf)
	if method != http.MethodGet {
		req.Header.Set("X-CSRF-Token", csrf.Value)
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}
```

- [ ] **Step 2: Run the lifecycle test**

Run:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  golang:1.25-bookworm \
  go test -race ./internal/app -run TestInitializationLifecycle -v
```

Expected: PASS.

- [ ] **Step 3: Verify no management secrets appear in logs or API responses**

Capture `slog` output during failed login, password change, and Profile creation. Use unique sentinel values and assert it does not contain:

```text
password-sentinel-6f27
authorization-sentinel-92c1
session-sentinel-1d54
csrf-sentinel-0b88
```

- [ ] **Step 4: Run all security and integration tests**

Run:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  golang:1.25-bookworm \
  sh -c 'go test -race ./internal/admin ./internal/app ./internal/gateway ./internal/proxy ./internal/vision'
```

Expected: PASS.

- [ ] **Step 5: Suggested commit checkpoint**

After explicit user approval:

```bash
git add internal/app/initialization_test.go
git commit -m "test: cover Profile initialization lifecycle"
```

---

### Task 8: Admin API phase verification

**Files:**
- Modify: `docs/development/profile-runtime.md`

- [ ] **Step 1: Document API-only initialization**

Add curl examples for:

- login;
- extracting cookies and CSRF;
- changing the default password;
- creating the first default Profile;
- listing Profiles.

State clearly that the Web UI arrives in the next plan.

- [ ] **Step 2: Validate Compose**

Run:

```bash
docker compose config --quiet
docker compose build
```

Expected: both exit `0`; the image contains no `/app/config.yaml`.

- [ ] **Step 3: Run full isolated verification**

Run:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  golang:1.25-bookworm \
  sh -c 'go test ./... && go test -race ./... && go vet ./... && CGO_ENABLED=0 GOOS=linux go build -o /tmp/llm-proxy ./cmd/llm-proxy'
```

Expected: all commands exit `0`.

- [ ] **Step 4: Inspect tracked changes**

Run:

```bash
git diff --check
git status --short
git diff --stat
```

Confirm no real `.env`, database, Session, or credential file is tracked.

- [ ] **Step 5: Suggested commit checkpoint**

After explicit user approval:

```bash
git add docs/development/profile-runtime.md
git commit -m "docs: describe Profile administration API"
```
