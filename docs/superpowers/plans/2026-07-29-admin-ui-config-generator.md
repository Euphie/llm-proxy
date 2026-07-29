# Admin UI and Configuration Generator Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver the embedded Web control panel, first-run workflow, Profile editor, filtered statistics, client configuration generator, final documentation, and removal of obsolete YAML assets.

**Architecture:** Serve a framework-free ES module application from an embedded filesystem under `/_admin`. Keep configuration generation as pure browser-side functions, call only same-origin admin APIs, and preserve the existing Go binary with no frontend build step.

**Tech Stack:** Go 1.25, embedded HTML/CSS/JavaScript, browser Fetch API, Node 24 built-in test runner, Playwright 1.62.0 in Docker, SQLite-backed admin API.

## Global Constraints

- Complete the Profile runtime and admin API plans first.
- Do not add React, Vue, a CSS framework, a CDN, or a JavaScript bundler.
- All assets are embedded in the Go binary and loaded only from `/_admin/assets/**`.
- Do not use inline scripts or inline styles; the existing CSP must remain valid without `'unsafe-inline'`.
- The UI never accepts a real API key or Authorization value.
- Model mapping fields are temporary browser state and are never sent to the server.
- Generated files are copied or downloaded by the browser; the server never writes client configuration.
- The Profile public URL defaults to `window.location.origin + "/" + profile.slug`.
- UI text is concise Chinese; protocol names, environment variables, paths, and error codes remain exact technical strings.
- Build and test JavaScript only inside Docker.
- Do not execute any `git commit` until the user explicitly authorizes commits; commit commands below are suggested checkpoints only.

---

### Task 1: Embedded admin application shell

**Files:**
- Create: `internal/admin/ui/embed.go`
- Create: `internal/admin/ui/embed_test.go`
- Create: `internal/admin/ui/static/index.html`
- Create: `internal/admin/ui/static/styles.css`
- Create: `internal/admin/ui/static/app.js`
- Create: `internal/admin/ui/static/api.js`
- Modify: `internal/app/app.go`
- Modify: `internal/app/app_test.go`

**Interfaces:**
- Consumes: admin API under `/_admin/api`.
- Produces: `adminui.NewHandler() http.Handler`
- Produces browser modules `api.js` and `app.js`.

- [ ] **Step 1: Write failing embedded-handler tests**

```go
func TestHandlerServesSPAAndAssets(t *testing.T) {
	handler := NewHandler()
	for _, tc := range []struct {
		path        string
		contentType string
		contains    string
	}{
		{"/_admin/", "text/html", `<main id="app">`},
		{"/_admin/profiles", "text/html", `<main id="app">`},
		{"/_admin/assets/app.js", "text/javascript", "bootstrap"},
		{"/_admin/assets/styles.css", "text/css", ":root"},
	} {
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if res.Code != http.StatusOK {
			t.Fatalf("%s: status=%d", tc.path, res.Code)
		}
		if !strings.Contains(res.Header().Get("Content-Type"), tc.contentType) ||
			!strings.Contains(res.Body.String(), tc.contains) {
			t.Fatalf("%s: type=%q body=%q", tc.path,
				res.Header().Get("Content-Type"), res.Body.String())
		}
	}
}
```

Also assert missing asset files return `404`, while non-asset admin paths return `index.html`.

- [ ] **Step 2: Run focused tests and verify failure**

Run:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  golang:1.25-bookworm \
  go test ./internal/admin/ui -v
```

Expected: FAIL because the UI package does not exist.

- [ ] **Step 3: Create the static shell**

`index.html` must contain:

```html
<!doctype html>
<html lang="zh-CN">
  <head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>llm-proxy</title>
    <link rel="stylesheet" href="/_admin/assets/styles.css">
  </head>
  <body>
    <main id="app" aria-live="polite"></main>
    <script type="module" src="/_admin/assets/app.js"></script>
  </body>
</html>
```

The CSS defines a neutral light theme, responsive navigation, form controls, cards, tables, badges, dialogs, error banners, and visible keyboard focus. Use system fonts and no external assets.

- [ ] **Step 4: Implement embedded routing**

Create:

```go
//go:embed static/*.html static/*.css static/*.js
var assets embed.FS

func NewHandler() http.Handler
```

Serve assets from the embedded subdirectory with correct MIME types and `Cache-Control: public, max-age=3600`. Serve `index.html` with `Cache-Control: no-store` for every other `/_admin/**` GET path. Reject non-GET methods with `405`.
Apply the same CSP, `X-Content-Type-Options`, `Referrer-Policy`, and
frame-ancestor protection used by the admin API to both HTML and asset
responses.

- [ ] **Step 5: Add the browser API client**

`api.js` exports:

```js
export function readCookie(name)
export async function request(path, options = {})
export const api = {
  session: () => request("/_admin/api/session"),
  login: (username, password) => request("/_admin/api/login", {
    method: "POST", body: { username, password },
  }),
  logout: () => request("/_admin/api/logout", { method: "POST" }),
  changePassword: (currentPassword, newPassword) =>
    request("/_admin/api/password", {
      method: "POST",
      body: {
        current_password: currentPassword,
        new_password: newPassword,
      },
    }),
}
```

For non-GET methods, read `llm_proxy_csrf` and set `X-CSRF-Token`. JSON-stringify object bodies. On non-2xx responses, throw an `APIError` containing status, code, message, and field errors.

- [ ] **Step 6: Mount UI without shadowing API**

In `internal/app/app.go`, register `/_admin/api/` first and the UI handler for the remaining `/_admin/` paths. Add an application test proving `/_admin/api/session` remains JSON and `/_admin/profiles` returns the SPA.

- [ ] **Step 7: Run Go UI tests**

Run:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  golang:1.25-bookworm \
  go test ./internal/admin/ui ./internal/app -v
```

Expected: PASS.

- [ ] **Step 8: Suggested commit checkpoint**

After explicit user approval:

```bash
git add internal/admin/ui internal/app
git commit -m "feat: embed admin application shell"
```

---

### Task 2: Login and first-run initialization UI

**Files:**
- Create: `internal/admin/ui/static/auth.js`
- Create: `internal/admin/ui/static/auth.test.mjs`
- Modify: `internal/admin/ui/static/app.js`
- Modify: `internal/admin/ui/static/styles.css`

**Interfaces:**
- Consumes: `api.session`, `api.login`, `api.changePassword`, and `api.logout`.
- Produces: `renderLogin`, `renderPasswordChange`, and authenticated app bootstrap.

- [ ] **Step 1: Write failing authentication state tests**

Create pure state functions and test:

```js
import test from "node:test";
import assert from "node:assert/strict";
import { nextScreen } from "./auth.js";

test("anonymous users see login", () => {
  assert.equal(nextScreen({ authenticated: false }), "login");
});

test("initial admin must change password", () => {
  assert.equal(nextScreen({
    authenticated: true,
    must_change_password: true,
  }), "password-change");
});

test("initialized admin reaches profiles", () => {
  assert.equal(nextScreen({
    authenticated: true,
    must_change_password: false,
  }), "profiles");
});
```

- [ ] **Step 2: Run Node test and verify failure**

Run:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  node:24-alpine \
  node --test internal/admin/ui/static/auth.test.mjs
```

Expected: FAIL because `auth.js` does not exist.

- [ ] **Step 3: Implement first-run screens**

`auth.js` exports:

```js
export function nextScreen(session)
export function renderLogin(root, handlers)
export function renderPasswordChange(root, handlers)
```

The login form has labeled username and password fields, defaults username to `admin`, never defaults the password field, and displays the public-initial-password warning before submission.

The password-change form:

- requires current password;
- requires the new password twice;
- enforces 10 characters client-side;
- never logs or persists field values;
- uses the rotated Session returned by the password API and redirects to
  Profile creation after success.

- [ ] **Step 4: Implement app bootstrap**

`app.js` must:

1. request `/_admin/api/session`;
2. render login on `401`;
3. render mandatory password change when `must_change_password=true`;
4. render the authenticated navigation otherwise;
5. clear the DOM before every screen transition;
6. display API errors in an `role="alert"` banner.

Navigation contains only `Profiles`, `统计`, `系统`, and `退出`.

- [ ] **Step 5: Run auth JS tests**

Run:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  node:24-alpine \
  node --test internal/admin/ui/static/auth.test.mjs
```

Expected: PASS.

- [ ] **Step 6: Run Go tests to verify embedding**

Run:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  golang:1.25-bookworm \
  go test ./internal/admin/ui ./internal/app
```

Expected: PASS.

- [ ] **Step 7: Suggested commit checkpoint**

After explicit user approval:

```bash
git add internal/admin/ui/static
git commit -m "feat: add first-run admin workflow"
```

---

### Task 3: Profile list and editor

**Files:**
- Create: `internal/admin/ui/static/profiles.js`
- Create: `internal/admin/ui/static/profiles.test.mjs`
- Modify: `internal/admin/ui/static/api.js`
- Modify: `internal/admin/ui/static/app.js`
- Modify: `internal/admin/ui/static/styles.css`

**Interfaces:**
- Consumes: Profile CRUD API.
- Produces: Profile list, editor, retry rule editor, copy, default, disable, and delete interactions.

- [ ] **Step 1: Extend the API client**

Add:

```js
Object.assign(api, {
  listProfiles: () => request("/_admin/api/profiles"),
  getProfile: (id) => request(`/_admin/api/profiles/${id}`),
  createProfile: (body) => request("/_admin/api/profiles", {
    method: "POST", body,
  }),
  updateProfile: (id, body) => request(`/_admin/api/profiles/${id}`, {
    method: "PUT", body,
  }),
  copyProfile: (id, body) => request(`/_admin/api/profiles/${id}/copy`, {
    method: "POST", body,
  }),
  deleteProfile: (id, replacementDefaultId = 0) =>
    request(`/_admin/api/profiles/${id}?replacement_default_id=${replacementDefaultId}`, {
      method: "DELETE",
    }),
  setDefaultProfile: (profileId) => request("/_admin/api/default-profile", {
    method: "PUT", body: { profile_id: profileId },
  }),
});
```

- [ ] **Step 2: Write failing form transformation tests**

```js
import test from "node:test";
import assert from "node:assert/strict";
import { profilePayload, defaultProfileDraft } from "./profiles.js";

test("new Anthropic Profile contains explicit defaults", () => {
  const draft = defaultProfileDraft("anthropic");
  assert.equal(draft.config.version, 1);
  assert.equal(draft.config.vision.model, "sonnet");
  assert.equal(draft.config.vision.timeout, "2m");
  assert.deepEqual(draft.config.overload_rules, []);
});

test("OpenAI draft disables vision", () => {
  const payload = profilePayload({
    ...defaultProfileDraft("openai"),
    slug: "openai",
    display_name: "OpenAI",
    upstream: "https://example.test",
  });
  assert.equal(payload.config.protocol, "openai");
  assert.equal(payload.config.vision.enabled, false);
});
```

Add tests preserving retry-rule order and omitting no configured field from the JSON payload.

- [ ] **Step 3: Run tests and verify failure**

Run:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  node:24-alpine \
  node --test internal/admin/ui/static/profiles.test.mjs
```

Expected: FAIL because `profiles.js` does not exist.

- [ ] **Step 4: Implement the Profile list**

`renderProfileList(root, data, actions)` displays:

- display name and slug;
- protocol and upstream;
- default, disabled, and vision badges;
- recent request and Token totals from the list response;
- buttons for edit, generate, copy, set default, disable/enable, and delete.

When no Profile exists, render only a primary `创建第一个 Profile` action and an explanation that proxy requests remain unavailable.

- [ ] **Step 5: Implement the Profile editor**

The editor has four sections:

```text
基础配置
视觉增强
容错规则
配置生成
```

Use native controls and explicit labels. Hide the vision section for OpenAI except for a note that vision preprocessing currently requires Anthropic. Keep advanced vision fields in a `<details>` element.

Retry rules support add, remove, and move up/down. Do not silently reorder rules because first match wins.

Before saving a changed slug, show:

```text
修改 slug 会改变 Agent 使用的 Profile URL。
```

- [ ] **Step 6: Implement destructive-action confirmation**

Use an accessible `<dialog>` for delete and default replacement. The UI must:

- prevent deleting the only Profile;
- require selecting another enabled Profile when deleting the default;
- never use `window.confirm`;
- display the exact Profile name and slug being deleted.

- [ ] **Step 7: Run Profile JS and Go tests**

Run:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  node:24-alpine \
  node --test internal/admin/ui/static/profiles.test.mjs
```

Then:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  golang:1.25-bookworm \
  go test ./internal/admin/ui ./internal/admin ./internal/app
```

Expected: PASS.

- [ ] **Step 8: Suggested commit checkpoint**

After explicit user approval:

```bash
git add internal/admin/ui/static
git commit -m "feat: manage Profiles from admin UI"
```

---

### Task 4: Browser-only client configuration generator

**Files:**
- Create: `internal/admin/ui/static/generator.js`
- Create: `internal/admin/ui/static/generator.test.mjs`
- Modify: `internal/admin/ui/static/profiles.js`
- Modify: `internal/admin/ui/static/styles.css`

**Interfaces:**
- Consumes: selected Profile data and temporary browser inputs.
- Produces: Claude Code settings JSON and Shell snippets without a server API.

- [ ] **Step 1: Write failing generator tests**

```js
import test from "node:test";
import assert from "node:assert/strict";
import {
  profileBaseURL,
  openAIBaseURL,
  claudeSettings,
  shellSnippet,
} from "./generator.js";

test("Profile base URL uses origin and slug", () => {
  assert.equal(
    profileBaseURL("https://proxy.example.com/", "coding"),
    "https://proxy.example.com/coding",
  );
});

test("OpenAI API base includes v1 below the Profile prefix", () => {
  assert.equal(
    openAIBaseURL("https://proxy.example.com/", "openai"),
    "https://proxy.example.com/openai/v1",
  );
});

test("Claude settings contain no real secret", () => {
  const output = claudeSettings({
    baseURL: "https://proxy.example.com/coding",
    models: { sonnet: "Kimi-K2.5", haiku: "", opus: "GLM-5" },
    authVariable: "ANTHROPIC_AUTH_TOKEN",
  });
  assert.deepEqual(output.env, {
    ANTHROPIC_BASE_URL: "https://proxy.example.com/coding",
    ANTHROPIC_DEFAULT_SONNET_MODEL: "Kimi-K2.5",
    ANTHROPIC_DEFAULT_OPUS_MODEL: "GLM-5",
    ANTHROPIC_AUTH_TOKEN: "<SET_LOCALLY>",
  });
});

test("Shell output safely quotes single quotes", () => {
  assert.equal(
    shellSnippet({ ANTHROPIC_BASE_URL: "https://example.test/a'b" }),
    `export ANTHROPIC_BASE_URL='https://example.test/a'\"'\"'b'`,
  );
});
```

- [ ] **Step 2: Run generator tests and verify failure**

Run:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  node:24-alpine \
  node --test internal/admin/ui/static/generator.test.mjs
```

Expected: FAIL because `generator.js` does not exist.

- [ ] **Step 3: Implement pure generator functions**

Export:

```js
export function profileBaseURL(origin, slug)
export function openAIBaseURL(origin, slug)
export function claudeSettings({ baseURL, models, authVariable })
export function openAIEnvironment({ baseURL, authVariable })
export function shellSnippet(environment)
export function downloadText(filename, content, mimeType)
```

Claude JSON must include:

```json
{
  "$schema": "https://json.schemastore.org/claude-code-settings.json",
  "env": {
    "ANTHROPIC_BASE_URL": "https://proxy.example.com/coding"
  }
}
```

Add only non-blank temporary model mappings. Authentication choices are `不生成`, `ANTHROPIC_AUTH_TOKEN`, and `ANTHROPIC_API_KEY`; selected variables use the literal placeholder `<SET_LOCALLY>`. The page must never provide a field for the real value.

- [ ] **Step 4: Implement generator UI**

For Anthropic Profiles, offer tabs:

```text
全局 ~/.claude/settings.json
项目共享 .claude/settings.json
项目私有 .claude/settings.local.json
Shell
```

The three JSON scopes share content but show different destination paths. Provide temporary inputs for public origin, sonnet, haiku, opus, and auth-variable placeholder type.

For OpenAI Profiles, show the Profile prefix and an OpenAI API base ending in
`/v1`, for example `https://proxy.example.com/openai/v1`, plus a generic
Shell snippet. Do not label it as Claude Code configuration.

Every output has `复制` and `下载` actions. Downloaded JSON uses `settings.json`; Shell uses `<slug>.env.sh`.

- [ ] **Step 5: Run generator tests**

Run:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  node:24-alpine \
  node --test internal/admin/ui/static/generator.test.mjs
```

Expected: PASS.

- [ ] **Step 6: Scan frontend for secret inputs**

Run:

```bash
rg -n 'type=\"password\"|api.?key|authorization|auth.?token' \
  internal/admin/ui/static
```

Expected: the only password inputs are login and password-change fields; generator references are labels and `<SET_LOCALLY>` output, not writable secret-value inputs.

- [ ] **Step 7: Suggested commit checkpoint**

After explicit user approval:

```bash
git add internal/admin/ui/static/generator* internal/admin/ui/static/profiles.js internal/admin/ui/static/styles.css
git commit -m "feat: generate client Profile configuration"
```

---

### Task 5: Statistics and system pages

**Files:**
- Create: `internal/admin/ui/static/stats.js`
- Create: `internal/admin/ui/static/stats.test.mjs`
- Create: `internal/admin/ui/static/system.js`
- Modify: `internal/admin/ui/static/api.js`
- Modify: `internal/admin/ui/static/app.js`
- Modify: `internal/admin/ui/static/styles.css`
- Modify: `internal/admin/system_handlers.go`
- Modify: `internal/admin/system_handlers_test.go`
- Modify: `internal/stats/stats.go`
- Delete: `internal/stats/ui.html`

**Interfaces:**
- Consumes: filtered stats and System API.
- Produces: Profile-aware statistics and database status pages.

- [ ] **Step 1: Extend API methods**

Add:

```js
Object.assign(api, {
  stats: (params) => request(`/_admin/api/stats?${new URLSearchParams(params)}`),
  system: () => request("/_admin/api/system"),
});
```

- [ ] **Step 2: Write failing statistics transformation tests**

```js
import test from "node:test";
import assert from "node:assert/strict";
import { normalizeFilters, tokenTotal } from "./stats.js";

test("empty filters are omitted", () => {
  assert.deepEqual(normalizeFilters({
    profile_id: "", protocol: "", model: "", kind: "",
    from: "", to: "",
  }), {});
});

test("token total excludes cache counters already represented in input", () => {
  assert.equal(tokenTotal({
    input_tokens: 10,
    output_tokens: 20,
    cache_read_tokens: 4,
    cache_creation_tokens: 5,
  }), 30);
});
```

- [ ] **Step 3: Implement the statistics page**

Provide filters for:

- Profile;
- protocol;
- model;
- request kind;
- from/to dates.

Render summary cards and accessible tables for day and model. Use CSS bars only as a secondary visual; all exact values remain in text. Distinguish `main` and `vision` with labels.

- [ ] **Step 4: Implement the System page**

The server returns only:

```json
{
  "version": "build version",
  "data_dir": "/app/data",
  "database_file": "llm-proxy.db",
  "database_bytes": 12345,
  "schema_version": 1,
  "default_profile_id": 1,
  "password_must_change": false
}
```

Do not return database connection strings, password fields, Session counts, or environment contents. The page renders these values and a password-change form.

Resolve `version` with `debug.ReadBuildInfo().Main.Version`, falling back to
`development`. Read `schema_version` from `PRAGMA user_version` and database
size with `os.Stat(filepath.Join(dataDir, "llm-proxy.db"))`. Inject `dataDir`
from `app.Options`; do not read or return the complete process environment.

Add `导出 Profiles` on the System page. It reuses `api.listProfiles()` in the
browser, removes usage summaries and database IDs, and downloads:

```json
{
  "version": 1,
  "profiles": [
    {
      "slug": "coding",
      "display_name": "Coding",
      "enabled": true,
      "default": true,
      "config": {}
    }
  ]
}
```

The export contains no statistics or authentication data. Do not implement
Profile import in this version.

- [ ] **Step 5: Remove the legacy stats UI**

Delete the embedded `internal/stats/ui.html` and remove `UIHandler` and the old public `Handler` if they remain. Statistics are available only through authenticated `/_admin/api/stats`.

- [ ] **Step 6: Run stats and System tests**

Run:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  node:24-alpine \
  node --test internal/admin/ui/static/stats.test.mjs
```

Then:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  golang:1.25-bookworm \
  go test ./internal/admin ./internal/stats ./internal/app
```

Expected: PASS.

- [ ] **Step 7: Suggested commit checkpoint**

After explicit user approval:

```bash
git add internal/admin internal/stats
git commit -m "feat: add Profile statistics and system UI"
```

---

### Task 6: Management lifecycle integration coverage

**Files:**
- Create: `internal/app/admin_ui_test.go`
- Modify: `Makefile`

**Interfaces:**
- Exercises the embedded SPA, API lifecycle, cookies, Profile hot reload, and persistence as one application.

- [ ] **Step 1: Add a static application smoke test**

Verify:

```go
func TestAdminUIAssetsAreServedByApplication(t *testing.T) {
	application, err := New(Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()

	for _, path := range []string{
		"/_admin/",
		"/_admin/assets/app.js",
		"/_admin/assets/styles.css",
		"/_admin/assets/generator.js",
	} {
		res := httptest.NewRecorder()
		application.Handler().ServeHTTP(
			res, httptest.NewRequest(http.MethodGet, path, nil),
		)
		if res.Code != http.StatusOK {
			t.Fatalf("%s: status=%d", path, res.Code)
		}
	}
}
```

- [ ] **Step 2: Add a restart persistence lifecycle test**

Use the API to change the password, create two Profiles, switch the default, and record usage. Close the application, reopen the same `DATA_DIR`, then assert:

- `admin/admin` no longer authenticates;
- the new password authenticates;
- both Profiles exist;
- root routing uses the selected default;
- historical usage remains queryable;
- old Session cookies no longer authenticate after password change.

- [ ] **Step 3: Add `test-ui` to Makefile**

```make
.PHONY: test-ui

test-ui:
	docker run --rm \
		-v "$(CURDIR)":/src:ro -w /src \
		node:24-alpine \
		node --test \
			internal/admin/ui/static/auth.test.mjs \
			internal/admin/ui/static/profiles.test.mjs \
			internal/admin/ui/static/generator.test.mjs \
			internal/admin/ui/static/stats.test.mjs
```

Keep the existing Go test target separate.

- [ ] **Step 4: Run lifecycle and frontend tests**

Run:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  golang:1.25-bookworm \
  go test -race ./internal/app -run 'TestAdminUI|TestRestartPersistence' -v
```

Then:

```bash
make test-ui
```

Expected: PASS.

- [ ] **Step 5: Suggested commit checkpoint**

After explicit user approval:

```bash
git add internal/app/admin_ui_test.go Makefile
git commit -m "test: cover admin UI lifecycle"
```

---

### Task 7: Real-browser first-run and Profile workflow

**Files:**
- Create: `e2e/package.json`
- Create: `e2e/package-lock.json`
- Create: `e2e/Dockerfile`
- Create: `e2e/playwright.config.js`
- Create: `e2e/admin.spec.js`
- Create: `docker-compose.e2e.yml`
- Modify: `Makefile`

**Interfaces:**
- Consumes: the built llm-proxy image and embedded admin UI.
- Produces: a reproducible Chromium workflow test in an isolated Compose project.

- [ ] **Step 1: Pin Playwright and its Docker image**

Create:

```json
{
  "name": "llm-proxy-e2e",
  "private": true,
  "type": "module",
  "devDependencies": {
    "@playwright/test": "1.62.0"
  }
}
```

Generate `package-lock.json` inside Docker:

```bash
docker run --rm \
  -v "$PWD/e2e":/work -w /work \
  node:24-bookworm \
  npm install --package-lock-only
```

Use the matching official image, as required by the
[Playwright Docker documentation](https://playwright.dev/docs/docker):

```dockerfile
FROM mcr.microsoft.com/playwright:v1.62.0-noble
WORKDIR /tests
COPY package.json package-lock.json ./
RUN npm ci
COPY playwright.config.js admin.spec.js ./
CMD ["npx", "playwright", "test"]
```

- [ ] **Step 2: Configure isolated Compose services**

Create `docker-compose.e2e.yml`:

```yaml
services:
  llm-proxy:
    ports: !reset []
    volumes: !reset []
    tmpfs:
      - /app/data
    environment:
      LISTEN: :8080
      DATA_DIR: /app/data

  e2e:
    build:
      context: ./e2e
    depends_on:
      - llm-proxy
    environment:
      BASE_URL: http://llm-proxy:8080
```

The override must not mount the user's real `DATA_DIR` and must not publish a host port.

- [ ] **Step 3: Configure Playwright**

Create:

```js
import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: ".",
  testMatch: "admin.spec.js",
  timeout: 30_000,
  fullyParallel: false,
  workers: 1,
  use: {
    baseURL: process.env.BASE_URL,
    browserName: "chromium",
    trace: "retain-on-failure",
  },
});
```

- [ ] **Step 4: Write the first-run browser test**

The Playwright test must:

```js
import { test, expect } from "@playwright/test";

test.beforeAll(async ({ request }) => {
  await expect.poll(async () => {
    try {
      return (await request.get("/_admin/")).status();
    } catch {
      return 0;
    }
  }).toBe(200);
});

test("initializes admin and creates the first Profile", async ({ page }) => {
  await page.goto("/_admin/");
  await expect(page.getByText("首次登录存在公网抢占风险")).toBeVisible();

  await page.getByLabel("用户名").fill("admin");
  await page.getByLabel("密码").fill("admin");
  await page.getByRole("button", { name: "登录" }).click();

  await expect(page.getByRole("heading", { name: "修改初始密码" })).toBeVisible();
  await page.getByLabel("当前密码").fill("admin");
  await page.getByLabel("新密码", { exact: true }).fill("secure-admin-2026");
  await page.getByLabel("确认新密码").fill("secure-admin-2026");
  await page.getByRole("button", { name: "保存新密码" }).click();

  await page.getByRole("button", { name: "创建第一个 Profile" }).click();
  await page.getByLabel("名称").fill("Coding");
  await page.getByLabel("Slug").fill("coding");
  await page.getByLabel("协议").selectOption("anthropic");
  await page.getByLabel("Upstream").fill("https://example.test/anthropic");
  await page.getByLabel("设为默认 Profile").check();
  await page.getByRole("button", { name: "保存 Profile" }).click();

  await expect(page.getByText("Coding")).toBeVisible();
  await expect(page.getByText("coding")).toBeVisible();
  await expect(page.getByText("默认")).toBeVisible();
});
```

Use exact accessible names in the UI implementation so the test needs no CSS selectors.

- [ ] **Step 5: Add generator and Session assertions**

In the same browser context:

- open `配置生成`;
- assert the initial URL equals `${BASE_URL}/coding`;
- enter temporary sonnet mapping `Kimi-K2.5`;
- choose the `ANTHROPIC_AUTH_TOKEN` placeholder;
- assert JSON includes `<SET_LOCALLY>` and never displays a secret-value input;
- reload and assert temporary model fields are blank;
- log out and assert `/_admin/profiles` returns to login.

- [ ] **Step 6: Add the isolated test target**

Add:

```make
.PHONY: test-e2e

test-e2e:
	@set -e; \
	trap 'docker compose -f docker-compose.yml -f docker-compose.e2e.yml -p llm-proxy-e2e down --volumes' EXIT; \
	docker compose \
		-f docker-compose.yml \
		-f docker-compose.e2e.yml \
		-p llm-proxy-e2e \
		up --build --abort-on-container-exit --exit-code-from e2e
```

The shell trap removes the isolated stack even when the browser test fails.

- [ ] **Step 7: Run browser E2E**

Run:

```bash
make test-e2e
```

Expected: Chromium completes the first-run, Profile, generator, reload, and logout flow with all assertions passing.

- [ ] **Step 8: Suggested commit checkpoint**

After explicit user approval:

```bash
git add e2e docker-compose.e2e.yml Makefile
git commit -m "test: cover admin workflow in Chromium"
```

---

### Task 8: Documentation, examples, and architecture diagram

**Files:**
- Modify: `README.md`
- Rewrite: `docs/configuration.md`
- Modify: `docs/vision.md`
- Rewrite: `docs/statistics.md`
- Create: `docs/profiles.md`
- Create: `docs/admin.md`
- Modify: `docs/assets/llm-proxy-architecture.excalidraw`
- Modify: `docs/assets/llm-proxy-architecture.svg`
- Delete: `config.yaml`
- Delete: `docs/development/profile-runtime.md`

**Interfaces:**
- Documents the final product only; removes transitional API-only instructions.

- [ ] **Step 1: Rewrite the README around the Profile console**

Lead with:

```markdown
# llm-proxy

面向 Agent 和 LLM SDK 的轻量级多 Profile 代理。

通过 Web 控制台管理上游协议、视觉增强和容错规则；客户端使用
简单的 Profile URL 选择运行配置，鉴权信息始终由请求透传。
```

Quick start must use only `.env`, `docker compose up -d --build`, `/_admin`, and the first-run `admin/admin` warning.

- [ ] **Step 2: Replace configuration documentation**

`docs/configuration.md` documents only:

```text
LISTEN
DATA_DIR
HOST
PORT
```

`docs/profiles.md` documents slug rules, protocols, upstream joining, default routing, named routing, hot reload, vision, and retry settings.

`docs/admin.md` documents first login, forced password change, public-bootstrap risk, Session behavior, backup, restore, and Profile export.

- [ ] **Step 3: Update feature documentation**

Update vision examples to refer to Profile fields rather than YAML. Update statistics to use `/_admin/stats` and Profile filters. Remove every mention of `/stats`, `/stats/data`, `stats_password`, `CONFIG_FILE`, and Provider YAML.

- [ ] **Step 4: Redraw the architecture diagram**

Use the `excalidraw` skill. Keep the existing clean grid style and redraw these aligned lanes:

```text
Agent / SDK
  → Profile Router
  → Runtime Registry
  → Vision / Retry / Stream
  → Upstream

/_admin
  → Auth + Profile API
  → SQLite
  → atomic Registry publish
```

Use straight or orthogonal arrows with strict bindings, no crossings, and separate control-plane and data-plane colors. Export both editable Excalidraw source and SVG, then render a PNG preview for visual inspection.

- [ ] **Step 5: Remove obsolete transitional files**

Delete tracked `config.yaml` and the internal transition note. Keep `.env` and `config-bak.yaml` ignored. Do not read, rewrite, or delete the user's ignored local files.

- [ ] **Step 6: Check documentation references**

Run:

```bash
rg -n 'CONFIG_FILE|stats_password|/stats/data|config\\.yaml|Provider YAML' \
  README.md docs .env.example Dockerfile docker-compose.yml Makefile
```

Expected: matches only in an explicit breaking-change note, not in active instructions.

Run:

```bash
git diff --check
```

Expected: no whitespace errors.

- [ ] **Step 7: Suggested commit checkpoint**

After explicit user approval:

```bash
git add README.md docs config.yaml
git commit -m "docs: describe Profile control console"
```

---

### Task 9: Final isolated verification

**Files:**
- Modify only files required by failures found during verification.

- [ ] **Step 1: Run all JavaScript tests**

Run:

```bash
make test-ui
```

Expected: all Node tests pass.

Run:

```bash
make test-e2e
```

Expected: the Playwright Chromium workflow passes in the isolated Compose project.

- [ ] **Step 2: Run all Go tests and static checks**

Run:

```bash
docker run --rm \
  -v "$PWD":/src:ro -w /src \
  golang:1.25-bookworm \
  sh -c 'go test ./... && go test -race ./... && go vet ./... && CGO_ENABLED=0 GOOS=linux go build -o /tmp/llm-proxy ./cmd/llm-proxy'
```

Expected: all commands exit `0`.

- [ ] **Step 3: Build and start the container**

Use a new temporary Compose project and data directory so the user's running service is untouched:

```bash
mkdir -p /tmp/llm-proxy-profile-smoke
DATA_DIR=/tmp/llm-proxy-profile-smoke \
PORT=18087 \
docker compose -p llm-proxy-profile-smoke up -d --build
```

Verify:

```bash
curl -fsS http://127.0.0.1:18087/_admin/ >/dev/null
curl -sS -o /tmp/llm-proxy-smoke-response \
  -w '%{http_code}' \
  http://127.0.0.1:18087/v1/messages
```

Expected: admin returns `200`; uninitialized proxy returns `503`.

- [ ] **Step 4: Stop and remove only the smoke stack**

```bash
DATA_DIR=/tmp/llm-proxy-profile-smoke \
PORT=18087 \
docker compose -p llm-proxy-profile-smoke down --volumes
```

Move `/tmp/llm-proxy-profile-smoke` and `/tmp/llm-proxy-smoke-response` to trash or remove only after confirming both exact paths belong to this smoke test.

- [ ] **Step 5: Audit secrets, obsolete names, and repository state**

Run:

```bash
git grep -n -I -E \
  'CONFIG_FILE|stats_password|statspwd123456|ANTHROPIC_API_KEY=.+' \
  -- ':!docs/superpowers/**'
```

Expected: no active runtime configuration or real key assignment remains.

Then run:

```bash
git diff --check
git status --short
git diff --stat
```

Expected: only intended Profile console changes are present; ignored `.env`, `config-bak.yaml`, data files, and Session data are absent.

- [ ] **Step 6: Suggested final commit checkpoint**

After explicit user approval:

```bash
git add -A
git commit -m "feat: add Profile management console"
```
