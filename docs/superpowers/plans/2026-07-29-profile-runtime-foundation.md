# Profile Runtime Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the SQLite schema, Profile domain, hot-swappable runtime registry, protocol-transparent routing, and Profile-aware usage recording without exposing the new control plane yet.

**Architecture:** Keep the current YAML entrypoint temporarily operational while moving proxy and vision code onto a new `profile.Runtime` type. Persist Profile records in a shared SQLite database, build one long-lived proxy handler per enabled Profile, and publish immutable registry snapshots atomically.

**Tech Stack:** Go 1.25, `net/http`, `database/sql`, `modernc.org/sqlite`, `sync/atomic`, existing Anthropic/OpenAI parsers.

## Global Constraints

- Run builds and tests in Docker; do not install dependencies into the host Go environment.
- Do not store API keys, Authorization values, request bodies, or custom authentication headers in SQLite.
- Profile slugs must match `[a-z0-9][a-z0-9-]{0,62}`, must not equal `v1`, and must not start with `_`.
- Supported Profile protocols are exactly `anthropic` and `openai`.
- Vision preprocessing is valid only for Anthropic Profiles and only on `POST /v1/messages`.
- Main requests forward end-to-end headers after removing hop-by-hop headers.
- Shadow vision requests forward only `Authorization`, `X-Api-Key`, and `Anthropic-Version`.
- Existing requests must continue on their old runtime when a Profile snapshot changes.
- Do not execute any `git commit` until the user explicitly authorizes commits; commit commands below are suggested checkpoints only.

---

### Task 1: Shared SQLite database and schema

**Files:**
- Create: `internal/database/database.go`
- Create: `internal/database/migrations.go`
- Create: `internal/database/database_test.go`
- Modify: `go.mod`

**Interfaces:**
- Produces: `database.Open(dataDir string) (*sql.DB, error)`
- Produces: `database.Migrate(db *sql.DB) error`
- Produces tables consumed by `profile.Store`, `admin` services, and `stats.DB`.

- [ ] **Step 1: Write failing database initialization tests**

```go
func TestOpenCreatesPrivateDatabaseAndSchema(t *testing.T) {
	db, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for _, table := range []string{
		"app_settings", "admin_account", "admin_sessions", "profiles", "usage",
	} {
		var name string
		err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`,
			table,
		).Scan(&name)
		if err != nil {
			t.Fatalf("table %s: %v", table, err)
		}
	}

	var journalMode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if strings.ToLower(journalMode) != "wal" {
		t.Fatalf("journal_mode=%q", journalMode)
	}
}
```

Also test that an empty `dataDir` is rejected and that `PRAGMA foreign_keys` returns `1`.

- [ ] **Step 2: Run the focused test and verify failure**

Run:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  golang:1.25-bookworm \
  go test ./internal/database -run TestOpen -v
```

Expected: FAIL because `internal/database` and `Open` do not exist.

- [ ] **Step 3: Implement database opening**

Create `internal/database/database.go` with:

```go
const filename = "llm-proxy.db"

func Open(dataDir string) (*sql.DB, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, errors.New("data directory is required")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	path := filepath.Join(dataDir, filename)
	query := url.Values{}
	query.Add("_pragma", "journal_mode(WAL)")
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", "busy_timeout(5000)")
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: query.Encode()}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect database: %w", err)
	}
	if err := Migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		db.Close()
		return nil, fmt.Errorf("secure database file: %w", err)
	}
	return db, nil
}
```

Import `modernc.org/sqlite` directly from this package.

- [ ] **Step 4: Implement schema migration**

Create `internal/database/migrations.go`. Migration version 1 must create:

```sql
CREATE TABLE admin_account (
    id                   INTEGER PRIMARY KEY CHECK (id = 1),
    username             TEXT NOT NULL UNIQUE,
    password_hash        TEXT NOT NULL,
    must_change_password INTEGER NOT NULL CHECK (must_change_password IN (0, 1)),
    auth_version         INTEGER NOT NULL DEFAULT 1,
    updated_at           TEXT NOT NULL
);

CREATE TABLE admin_sessions (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    token_hash   BLOB NOT NULL UNIQUE,
    csrf_hash    BLOB NOT NULL,
    auth_version INTEGER NOT NULL,
    created_at   TEXT NOT NULL,
    expires_at   TEXT NOT NULL
);

CREATE TABLE profiles (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    slug         TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL,
    enabled      INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    config_json  TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL
);

CREATE TABLE app_settings (
    id                 INTEGER PRIMARY KEY CHECK (id = 1),
    default_profile_id INTEGER REFERENCES profiles(id),
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL
);

CREATE TABLE usage (
    id                    INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at            TEXT NOT NULL,
    profile_id            INTEGER REFERENCES profiles(id) ON DELETE SET NULL,
    profile_slug          TEXT NOT NULL,
    protocol              TEXT NOT NULL,
    request_kind          TEXT NOT NULL CHECK (request_kind IN ('main', 'vision')),
    model                 TEXT NOT NULL DEFAULT '',
    path                  TEXT NOT NULL DEFAULT '',
    input_tokens          INTEGER NOT NULL DEFAULT 0,
    output_tokens         INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens     INTEGER NOT NULL DEFAULT 0,
    cache_creation_tokens INTEGER NOT NULL DEFAULT 0
);
```

Create indexes for usage date, Profile, model, and request kind. Insert the singleton `app_settings` row during migration. Apply the migration inside a transaction and set `PRAGMA user_version=1` only after all statements succeed.

- [ ] **Step 5: Run database tests**

Run:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  golang:1.25-bookworm \
  go test ./internal/database -v
```

Expected: PASS, including WAL, foreign-key, file-mode, and idempotent reopen tests.

- [ ] **Step 6: Suggested commit checkpoint**

After explicit user approval:

```bash
git add go.mod go.sum internal/database
git commit -m "feat: add shared SQLite database"
```

---

### Task 2: Profile domain and runtime resolution

**Files:**
- Create: `internal/profile/model.go`
- Create: `internal/profile/model_test.go`

**Interfaces:**
- Produces: `profile.Record`, `profile.Config`, `profile.Runtime`
- Produces: `profile.NewConfig(protocol Protocol, upstream string) Config`
- Produces: `func (Record) Resolve() (Runtime, error)`
- Consumed later by `profile.Store`, `proxy.New`, `gateway.Registry`, and admin handlers.

- [ ] **Step 1: Write failing validation and default tests**

```go
func TestRecordResolveAppliesRuntimeValues(t *testing.T) {
	record := Record{
		ID: 7, Slug: "coding", DisplayName: "Coding", Enabled: true,
		Config: NewConfig(ProtocolAnthropic, "https://example.test/anthropic"),
	}
	runtime, err := record.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if runtime.ID != 7 || runtime.Slug != "coding" {
		t.Fatalf("identity=%+v", runtime)
	}
	if runtime.Vision.Model != "sonnet" ||
		runtime.Vision.MaxTokens != 2048 ||
		runtime.Vision.Timeout != 2*time.Minute ||
		runtime.Vision.CacheTTL != 30*time.Minute {
		t.Fatalf("vision defaults=%+v", runtime.Vision)
	}
}

func TestRecordResolveRejectsReservedSlug(t *testing.T) {
	record := Record{
		Slug: "v1", DisplayName: "Reserved", Enabled: true,
		Config: NewConfig(ProtocolAnthropic, "https://example.test"),
	}
	if _, err := record.Resolve(); !errors.Is(err, ErrInvalidSlug) {
		t.Fatalf("error=%v", err)
	}
}
```

Add table tests for `_admin`, uppercase, spaces, a 64-character slug, unsupported protocol, upstream credentials, upstream query/fragment, OpenAI with vision enabled, zero numeric limits, and invalid duration strings.

- [ ] **Step 2: Run focused tests and verify failure**

Run:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  golang:1.25-bookworm \
  go test ./internal/profile -run 'TestRecordResolve' -v
```

Expected: FAIL because the Profile types do not exist.

- [ ] **Step 3: Define persistent and runtime types**

Create these exact public types:

```go
type Protocol string

const (
	ProtocolAnthropic Protocol = "anthropic"
	ProtocolOpenAI    Protocol = "openai"
)

type VisionConfig struct {
	Enabled         bool   `json:"enabled"`
	Model           string `json:"model"`
	MaxTokens       int    `json:"max_tokens"`
	Timeout         string `json:"timeout"`
	MaxConcurrency  int    `json:"max_concurrency"`
	CacheTTL        string `json:"cache_ttl"`
	CacheMaxEntries int    `json:"cache_max_entries"`
	Prompt          string `json:"prompt,omitempty"`
}

type RetryRule struct {
	Status       int    `json:"status"`
	BodyContains string `json:"body_contains,omitempty"`
	MaxRetries   int    `json:"max_retries"`
	Delay        string `json:"delay"`
	Jitter       string `json:"jitter"`
}

type Config struct {
	Version       int          `json:"version"`
	Protocol      Protocol     `json:"protocol"`
	Upstream      string       `json:"upstream"`
	Vision        VisionConfig `json:"vision"`
	OverloadRules []RetryRule  `json:"overload_rules"`
}

type Record struct {
	ID          int64
	Slug        string
	DisplayName string
	Enabled     bool
	Config      Config
	CreatedAt   time.Time
	UpdatedAt   time.Time
}
```

`Runtime` must carry parsed `time.Duration` values and `[]provider.Rule`, while keeping `Upstream` as a validated string without a trailing slash.

- [ ] **Step 4: Implement defaults and validation**

`NewConfig` must return version `1` and these exact defaults:

```go
VisionConfig{
	Enabled: false, Model: "sonnet", MaxTokens: 2048,
	Timeout: "2m", MaxConcurrency: 4,
	CacheTTL: "30m", CacheMaxEntries: 512,
}
```

`Record.Resolve` must:

- validate slug with `^[a-z0-9][a-z0-9-]{0,62}$`;
- reject `v1`;
- require a non-blank display name;
- accept only the two protocol constants;
- parse upstream with `url.ParseRequestURI`;
- require `http` or `https`, a host, no userinfo, no query, and no fragment;
- parse all durations with `time.ParseDuration`;
- require positive vision limits when vision is enabled;
- reject vision on OpenAI;
- convert retry rules to `provider.Rule`.

Expose sentinel errors `ErrInvalidSlug`, `ErrInvalidProtocol`, and `ErrInvalidConfig` so the admin API can map validation failures without string matching.

- [ ] **Step 5: Run Profile domain tests**

Run:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  golang:1.25-bookworm \
  go test ./internal/profile -v
```

Expected: PASS.

- [ ] **Step 6: Suggested commit checkpoint**

After explicit user approval:

```bash
git add internal/profile/model.go internal/profile/model_test.go
git commit -m "feat: define Profile runtime configuration"
```

---

### Task 3: Transactional Profile store

**Files:**
- Create: `internal/profile/store.go`
- Create: `internal/profile/store_test.go`

**Interfaces:**
- Consumes: schema from Task 1 and `profile.Record` from Task 2.
- Produces: `profile.NewStore(db *sql.DB) *Store`
- Produces: `Save`, `LoadSnapshot`, `Get`, `Copy`, `SetDefault`, and `Delete`.

- [ ] **Step 1: Write failing store lifecycle tests**

```go
func TestStoreSaveDefaultCopyAndDelete(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := NewStore(db)
	ctx := context.Background()

	first, err := store.Save(ctx, SaveInput{
		Slug: "coding", DisplayName: "Coding", Enabled: true,
		Config: NewConfig(ProtocolAnthropic, "https://example.test"),
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	records, defaultID, err := store.LoadSnapshot(ctx)
	if err != nil || len(records) != 1 || defaultID != first.ID {
		t.Fatalf("snapshot records=%v default=%d err=%v", records, defaultID, err)
	}

	copy, err := store.Copy(ctx, first.ID, "coding-copy", "Coding Copy")
	if err != nil {
		t.Fatal(err)
	}
	if copy.ID == first.ID || copy.Config.Upstream != first.Config.Upstream {
		t.Fatalf("copy=%+v", copy)
	}
	if err := store.Delete(ctx, first.ID, copy.ID); err != nil {
		t.Fatal(err)
	}
}
```

Add tests for duplicate slug rollback, updating a record, deleting a non-default Profile with replacement `0`, rejecting deletion of the default without a valid replacement, rejecting a disabled default, and retaining usage rows when a Profile is deleted.

- [ ] **Step 2: Run store tests and verify failure**

Run:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  golang:1.25-bookworm \
  go test ./internal/profile -run 'TestStore' -v
```

Expected: FAIL because `Store` and `SaveInput` do not exist.

- [ ] **Step 3: Implement store interfaces**

Use these exact signatures:

```go
type SaveInput struct {
	ID          int64
	Slug        string
	DisplayName string
	Enabled     bool
	Config      Config
}

func NewStore(db *sql.DB) *Store
func (s *Store) Save(ctx context.Context, input SaveInput, makeDefault bool) (Record, error)
func (s *Store) LoadSnapshot(ctx context.Context) ([]Record, int64, error)
func (s *Store) Get(ctx context.Context, id int64) (Record, error)
func (s *Store) Copy(ctx context.Context, id int64, slug, displayName string) (Record, error)
func (s *Store) SetDefault(ctx context.Context, id int64) error
func (s *Store) Delete(ctx context.Context, id, replacementDefaultID int64) error
```

Validate by constructing a `Record` and calling `Resolve` before opening a write transaction. Encode `Config` with `json.Marshal`; reject JSON versions other than `1` while loading.

- [ ] **Step 4: Implement atomic default invariants**

Inside `Save`, `SetDefault`, and `Delete`:

- use `sql.Tx`;
- verify a new default exists and is enabled;
- reject disabling the current default;
- change `app_settings.default_profile_id` in the same transaction as insert, update, or delete;
- translate SQLite unique-slug failures to `ErrSlugConflict`;
- return `ErrNotFound` and `ErrDefaultRequired` as sentinel errors.

Use RFC3339Nano UTC timestamps for all persisted time values.

- [ ] **Step 5: Run Profile package tests**

Run:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  golang:1.25-bookworm \
  go test ./internal/profile -v
```

Expected: PASS.

- [ ] **Step 6: Suggested commit checkpoint**

After explicit user approval:

```bash
git add internal/profile/store.go internal/profile/store_test.go
git commit -m "feat: persist Profile configuration"
```

---

### Task 4: Profile-aware usage store

**Files:**
- Modify: `internal/stats/stats.go`
- Create: `internal/stats/query.go`
- Create: `internal/stats/stats_test.go`
- Modify: `internal/stats/anthropic_test.go`
- Modify: `internal/vision/client_test.go`

**Interfaces:**
- Consumes: shared `*sql.DB` from `database.Open`.
- Produces: `stats.New(db *sql.DB) *DB`
- Produces: `stats.RequestMeta`, `stats.Filter`, `stats.Response`, `stats.ProfileSummary`
- Produces: `RecordAsync(meta RequestMeta, data []byte, parser Parser)`.

- [ ] **Step 1: Write failing Profile-aware usage tests**

```go
func TestRecordAsyncStoresProfileMetadata(t *testing.T) {
	db, err := database.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := New(db)
	done := make(chan struct{}, 1)
	store.afterWrite = func() { done <- struct{}{} }

	store.RecordAsync(RequestMeta{
		ProfileID: 9, ProfileSlug: "coding", Protocol: "anthropic",
		Kind: "vision", Path: "/v1/messages",
	}, []byte(`{"model":"sonnet","usage":{"input_tokens":10,"output_tokens":20}}`), AnthropicParser{})

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("usage write timed out")
	}

	var slug, protocol, kind string
	err = db.QueryRow(`SELECT profile_slug, protocol, request_kind FROM usage`).Scan(
		&slug, &protocol, &kind,
	)
	if err != nil || slug != "coding" || protocol != "anthropic" || kind != "vision" {
		t.Fatalf("row=%q %q %q err=%v", slug, protocol, kind, err)
	}
}
```

Add query tests filtering by Profile, protocol, kind, model, and inclusive UTC time bounds. Add a Profile-summary test that aggregates the last 30 days of requests and tokens into a map keyed by `profile_id`.

- [ ] **Step 2: Run focused stats tests and verify failure**

Run:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  golang:1.25-bookworm \
  go test ./internal/stats -run 'TestRecordAsync|TestQuery' -v
```

Expected: FAIL because the new constructor and metadata types do not exist.

- [ ] **Step 3: Refactor recording onto the shared database**

Remove the old stats-only schema and package-level migration. Add:

```go
type RequestMeta struct {
	ProfileID   int64
	ProfileSlug string
	Protocol    string
	Kind        string
	Path        string
}

func New(db *sql.DB) *DB {
	return &DB{db: db}
}

func (s *DB) RecordAsync(meta RequestMeta, data []byte, p Parser)
```

Insert every `RequestMeta` field into the new usage columns. Resolve
`profile_id` inside the statement:

```sql
(SELECT id FROM profiles WHERE id = ?)
```

This stores SQL `NULL` for the temporary YAML adapter (`ProfileID=0`) and for
an in-flight request whose Profile was deleted before usage recording, while
still preserving `profile_slug`. Skip writes when parsing returns `ok=false`;
log database errors without returning them to the proxy.

Retain `stats.Open(path)` and `Close()` only as a transitional adapter for the
current YAML entrypoint. Reimplement `Open` on the exact requested SQLite path,
apply the same connection pragmas, call `database.Migrate`, and return
`stats.New(db)`. Plan 2 deletes this adapter when the application switches to
`DATA_DIR`.

- [ ] **Step 4: Add filtered queries**

Create:

```go
type Filter struct {
	ProfileID *int64
	Protocol  string
	Model     string
	Kind      string
	From      time.Time
	To        time.Time
}

type Response struct {
	Summary UsageRow   `json:"summary"`
	ByDay   []UsageRow `json:"by_day"`
	ByModel []UsageRow `json:"by_model"`
}

func (s *DB) Query(ctx context.Context, filter Filter) (Response, error)

type ProfileSummary struct {
	Requests     int `json:"requests"`
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

func (s *DB) ProfileSummaries(
	ctx context.Context,
	since time.Time,
) (map[int64]ProfileSummary, error)
```

Build SQL predicates with positional arguments; never interpolate filter values into SQL. Preserve the existing summary, day, and model totals.

- [ ] **Step 5: Update existing tests and run stats tests**

Replace every `stats.Open(path)` in tests with:

```go
db, err := database.Open(t.TempDir())
if err != nil {
	t.Fatal(err)
}
defer db.Close()
sdb := stats.New(db)
```

Run:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  golang:1.25-bookworm \
  go test ./internal/stats ./internal/vision -v
```

Expected: PASS.

- [ ] **Step 6: Suggested commit checkpoint**

After explicit user approval:

```bash
git add internal/stats internal/vision/client_test.go
git commit -m "feat: record usage by Profile"
```

---

### Task 5: Move proxy and vision onto `profile.Runtime`

**Files:**
- Modify: `internal/proxy/proxy.go`
- Modify: `internal/proxy/proxy_test.go`
- Modify: `internal/vision/client.go`
- Modify: `internal/vision/client_test.go`
- Modify: `internal/vision/preprocessor.go`
- Modify: `internal/vision/preprocessor_test.go`
- Modify: `internal/config/config.go`
- Modify: `cmd/llm-proxy/main.go`

**Interfaces:**
- Consumes: `profile.Runtime` and `stats.RequestMeta`.
- Produces: `proxy.New(cfg profile.Runtime, client *http.Client, stats *stats.DB) http.Handler`
- Produces transitional `config.Config.Runtime() profile.Runtime` for the old YAML entrypoint.

- [ ] **Step 1: Write failing target and header tests**

```go
func TestProxyPreservesEscapedPathQueryAndFiltersHopHeaders(t *testing.T) {
	var gotURI string
	var gotHeader http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURI = r.RequestURI
		gotHeader = r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	record := profile.Record{
		ID: 4, Slug: "coding", DisplayName: "Coding", Enabled: true,
		Config: profile.NewConfig(profile.ProtocolAnthropic, upstream.URL+"/base"),
	}
	runtime, err := record.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	handler := New(runtime, upstream.Client(), nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/files/a%2Fb?download=1", nil)
	req.Header.Set("Connection", "keep-alive, X-Remove")
	req.Header.Set("X-Remove", "secret")
	req.Header.Set("X-Keep", "value")
	res := httptest.NewRecorder()

	handler.ServeHTTP(res, req)

	if gotURI != "/base/v1/files/a%2Fb?download=1" {
		t.Fatalf("requestURI=%q", gotURI)
	}
	if gotHeader.Get("Connection") != "" || gotHeader.Get("X-Remove") != "" {
		t.Fatalf("hop headers=%v", gotHeader)
	}
	if gotHeader.Get("X-Keep") != "value" {
		t.Fatalf("end-to-end header=%q", gotHeader.Get("X-Keep"))
	}
}
```

Add a test asserting main usage records `ProfileID`, slug, protocol, `Kind=main`, and the stripped path.

- [ ] **Step 2: Run proxy tests and verify failure**

Run:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  golang:1.25-bookworm \
  go test ./internal/proxy -run 'TestProxyPreserves|TestProxyRecords' -v
```

Expected: FAIL because `proxy.New` still accepts `*config.Config` and forwards hop-by-hop headers.

- [ ] **Step 3: Change runtime dependencies**

Change the constructor to:

```go
func New(cfg profile.Runtime, client *http.Client, sdb *stats.DB) http.Handler
```

Update `vision.New`, `newVisionClient`, and all tests to consume `profile.Runtime` or `profile.VisionRuntime`. Replace provider-oriented log fields with `profile`.

Add a transitional method in `internal/config`:

```go
func (c *Config) Runtime() profile.Runtime
```

It must map the current resolved YAML configuration to Profile ID `0`, slug/name `c.ProviderName`, and the existing retry and vision values. The old entrypoint calls `proxy.New(cfg.Runtime(), client, sdb)` until Plan 2 removes YAML.

- [ ] **Step 4: Implement safe URL and header forwarding**

Build targets with:

```go
func targetURL(upstream, requestURI string) string {
	return strings.TrimRight(upstream, "/") + "/" + strings.TrimLeft(requestURI, "/")
}
```

Profile validation already forbids upstream query and fragment, so concatenating the untouched `RequestURI` preserves escaped path segments and the raw query.

Implement RFC hop-by-hop removal for:

```text
Connection
Proxy-Connection
Keep-Alive
Proxy-Authenticate
Proxy-Authorization
TE
Trailer
Transfer-Encoding
Upgrade
```

Also remove every header named by tokens in the incoming `Connection` header.
Apply the same hop-by-hop filtering to upstream response headers before writing them to the client.

- [ ] **Step 5: Wire Profile-aware usage**

The main proxy records:

```go
stats.RequestMeta{
	ProfileID: cfg.ID, ProfileSlug: cfg.Slug,
	Protocol: string(cfg.Protocol), Kind: "main", Path: r.URL.Path,
}
```

The vision client records the same identity with `Kind: "vision"` and `Path: "/v1/messages"`.

- [ ] **Step 6: Run affected package tests**

Run:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  golang:1.25-bookworm \
  sh -c 'go test ./internal/config ./internal/proxy ./internal/vision ./internal/stats ./cmd/llm-proxy'
```

Expected: PASS.

- [ ] **Step 7: Suggested commit checkpoint**

After explicit user approval:

```bash
git add cmd/llm-proxy internal/config internal/proxy internal/vision internal/stats
git commit -m "refactor: use Profile runtime in proxy"
```

---

### Task 6: Atomic runtime registry and Profile router

**Files:**
- Create: `internal/gateway/registry.go`
- Create: `internal/gateway/registry_test.go`
- Create: `internal/gateway/router.go`
- Create: `internal/gateway/router_test.go`

**Interfaces:**
- Consumes: `profile.Record` and a Profile handler builder.
- Produces: `gateway.Builder`
- Produces: `gateway.NewRegistry() *Registry`
- Produces: `Load`, `Upsert`, `Delete`, and `gateway.NewRouter`.

- [ ] **Step 1: Write failing routing tests**

```go
func TestRouterSelectsDefaultAndNamedProfiles(t *testing.T) {
	registry := NewRegistry()
	builder := func(record profile.Record) (http.Handler, error) {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, record.Slug+" "+r.RequestURI)
		}), nil
	}
	records := []profile.Record{
		testRecord(1, "default", true),
		testRecord(2, "coding", true),
	}
	if err := registry.Load(records, 1, builder); err != nil {
		t.Fatal(err)
	}
	router := NewRouter(registry)

	for _, tc := range []struct {
		path string
		want string
	}{
		{"/v1/messages?x=1", "default /v1/messages?x=1"},
		{"/coding/v1/messages?x=1", "coding /v1/messages?x=1"},
	} {
		req := httptest.NewRequest(http.MethodPost, tc.path, nil)
		res := httptest.NewRecorder()
		router.ServeHTTP(res, req)
		if res.Code != http.StatusOK || res.Body.String() != tc.want {
			t.Fatalf("%s: status=%d body=%q", tc.path, res.Code, res.Body.String())
		}
	}
}
```

Add tests for `503 proxy_not_configured`, missing Profile, disabled Profile, reserved `/_admin` not being consumed, raw escaped path preservation, and hot replacement while an old request is blocked.

- [ ] **Step 2: Run gateway tests and verify failure**

Run:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  golang:1.25-bookworm \
  go test ./internal/gateway -v
```

Expected: FAIL because `internal/gateway` does not exist.

- [ ] **Step 3: Implement immutable registry snapshots**

Define:

```go
type Builder func(profile.Record) (http.Handler, error)

type runtime struct {
	record  profile.Record
	handler http.Handler
}

type snapshot struct {
	defaultID int64
	byID      map[int64]*runtime
	bySlug    map[string]*runtime
}

type Registry struct {
	current atomic.Pointer[snapshot]
}

func NewRegistry() *Registry
func (r *Registry) Load(records []profile.Record, defaultID int64, build Builder) error
func (r *Registry) Upsert(record profile.Record, defaultID int64, build Builder) error
func (r *Registry) Delete(id, defaultID int64)
```

`Load` builds every enabled handler before publishing. `Upsert` clones both maps, removes the previous slug associated with the same Profile ID, rebuilds only the changed Profile, preserves every unchanged runtime, then stores the new snapshot once. Disabled Profiles remain addressable in the snapshot with a nil handler so the router can return `profile_disabled`.

- [ ] **Step 4: Implement router path handling**

`NewRouter(registry *Registry)` returns an `http.Handler` with these rules:

```text
/_admin or /_admin/**  -> 404 from the data router; outer app mux owns it
/v1 or /v1/**         -> default Profile, path unchanged
/{slug}/**             -> named Profile, first path segment removed
```

Clone the request before changing `URL.Path`, `URL.RawPath`, and `RequestURI`. Preserve the query exactly. Return JSON errors with:

```json
{"error":{"code":"profile_not_found","message":"Profile not found"}}
```

Use the same envelope for `proxy_not_configured` and `profile_disabled`.

- [ ] **Step 5: Prove in-flight request isolation**

In `registry_test.go`, make the old handler block on a channel, call `Upsert` with a new handler, assert a second request uses the new handler, then release the first request and assert it completes with the old response. Run with:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  golang:1.25-bookworm \
  go test -race ./internal/gateway -run TestRegistryInFlightRequestKeepsOldRuntime -v
```

Expected: PASS with no race report.

- [ ] **Step 6: Run all foundation tests**

Run:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  golang:1.25-bookworm \
  sh -c 'go test ./... && go test -race ./internal/database ./internal/profile ./internal/gateway ./internal/proxy ./internal/stats ./internal/vision'
```

Expected: PASS.

- [ ] **Step 7: Suggested commit checkpoint**

After explicit user approval:

```bash
git add internal/gateway
git commit -m "feat: route requests through Profile registry"
```

---

### Task 7: Foundation verification and handoff contract

**Files:**
- Create: `docs/development/profile-runtime.md`
- Modify: `README.md`

**Interfaces:**
- Documents exact interfaces consumed by the admin/control-plane plan.
- Does not expose the unfinished Profile router from the production entrypoint.

- [ ] **Step 1: Document the internal handoff**

Create `docs/development/profile-runtime.md` with:

```markdown
# Profile runtime internals

The control plane persists Profiles through `profile.Store`.
After a successful transaction it calls `gateway.Registry.Upsert` or
`gateway.Registry.Delete`. The gateway owns one long-lived proxy handler
per enabled Profile, so vision caches and concurrency limits are isolated
by Profile. The production entrypoint remains YAML-backed until the admin
API plan switches startup to `DATA_DIR`.
```

List the exact public signatures from Tasks 2, 3, 4, and 6 beneath that paragraph.

- [ ] **Step 2: Add a README development note**

Add a short development-only note linking to `docs/development/profile-runtime.md`; do not advertise the unfinished admin feature as available.

- [ ] **Step 3: Run final isolated verification**

Run:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  golang:1.25-bookworm \
  sh -c 'go test ./... && go test -race ./... && go vet ./...'
```

Then run:

```bash
docker build -t llm-proxy:profile-foundation .
```

Expected: all commands exit `0`.

- [ ] **Step 4: Inspect the change set**

Run:

```bash
git diff --check
git status --short
git diff --stat
```

Expected: only foundation code, tests, and the internal development note are changed.

- [ ] **Step 5: Suggested commit checkpoint**

After explicit user approval:

```bash
git add README.md docs/development internal
git commit -m "docs: describe Profile runtime foundation"
```
