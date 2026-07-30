import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

import * as generator from "./generator.js";
import {
  claudeSettings,
  downloadText,
  openConfigurationGenerator,
  openAIBaseURL,
  openAIEnvironment,
  profileBaseURL,
  shellSnippet,
} from "./generator.js";

const stableOpenCodeModelLimitSchema = JSON.parse(
  readFileSync(
    new URL(
      "./testdata/opencode-provider-model-limit-v1.18.4.schema.json",
      import.meta.url,
    ),
    "utf8",
  ),
);
const stableOpenCodeModelLimitSource =
  "https://raw.githubusercontent.com/anomalyco/opencode/v1.18.4/packages/core/src/v1/config/provider.ts";

test("agent adapters are fixed by Profile protocol", () => {
  assert.equal(typeof generator.availableAgentAdapters, "function");
  assert.deepEqual(generator.availableAgentAdapters("anthropic"), [
    ["claude-code", "Claude Code"],
    ["opencode-anthropic", "OpenCode (Anthropic)"],
    ["generic", "Generic"],
  ]);
  assert.deepEqual(generator.availableAgentAdapters("openai"), [
    ["generic", "Generic"],
    ["opencode-responses", "OpenCode (Responses)"],
    ["codex-responses", "Codex (Responses)"],
  ]);
});

test("compaction calculation reserves output tokens without exceeding the requested percentage", () => {
  assert.equal(typeof generator.calculateCompaction, "function");
  assert.deepEqual(
    generator.calculateCompaction(
      { context_window: 128000, max_output_tokens: 16000 },
      85,
    ),
    {
      contextWindow: 128000,
      compactAt: 108800,
      reserved: 19200,
      percent: 85,
    },
  );
  assert.deepEqual(
    generator.calculateCompaction(
      { context_window: 100000, max_output_tokens: 30000 },
      85,
    ),
    {
      contextWindow: 100000,
      compactAt: 70000,
      reserved: 30000,
      percent: 85,
    },
  );
});

test("compaction calculation stays exact at the safe integer boundary", () => {
  assert.deepEqual(
    generator.calculateCompaction(
      { context_window: 9007199254740990 },
      85,
    ),
    {
      contextWindow: 9007199254740990,
      compactAt: 7656119366529841,
      reserved: 1351079888211149,
      percent: 85,
    },
  );
});

test("compaction calculation rejects invalid percentages and unusable model facts", () => {
  for (const percent of [0, 100, 85.5]) {
    assert.throws(
      () =>
        generator.calculateCompaction(
          { context_window: 128000 },
          percent,
        ),
      /percent/i,
    );
  }
  assert.throws(
    () => generator.calculateCompaction({}, 85),
    /context window/i,
  );
  assert.throws(
    () =>
      generator.calculateCompaction(
        { context_window: 100, max_output_tokens: 100 },
        85,
      ),
    /positive/i,
  );
  for (const model of [
    { context_window: Number.MAX_SAFE_INTEGER + 1 },
    {
      context_window: Number.MAX_SAFE_INTEGER,
      max_output_tokens: Number.MAX_SAFE_INTEGER + 1,
    },
  ]) {
    assert.throws(
      () => generator.calculateCompaction(model, 85),
      /safe integer/i,
    );
  }
});

test("Claude context environment derives one conservative override from every mapped model", () => {
  assert.equal(typeof generator.claudeContextEnvironment, "function");
  const standard = generator.claudeContextEnvironment(
    [
      {
        id: "sonnet-model",
        context_window: 128000,
        max_output_tokens: 16000,
        supports_vision: true,
      },
      {
        id: "haiku-model",
        context_window: 200000,
        max_output_tokens: 20000,
        supports_vision: false,
      },
      {
        id: "opus-model",
        context_window: 300000,
        max_output_tokens: 30000,
        supports_vision: true,
      },
    ],
    {
      sonnet: "sonnet-model",
      haiku: "haiku-model",
      opus: "opus-model",
    },
    85,
  );
  assert.deepEqual(standard.environment, {
    CLAUDE_CODE_AUTO_COMPACT_WINDOW: "128000",
    CLAUDE_AUTOCOMPACT_PCT_OVERRIDE: "85",
  });
  assert.equal(standard.warning, "");
  assert.deepEqual(standard.compaction, {
    contextWindow: 128000,
    compactAt: 108800,
    reserved: 19200,
    percent: 85,
    safeCompactAt: 108800,
    outputReserveLimited: false,
  });

  const conservative = generator.claudeContextEnvironment(
    [
      {
        id: "sonnet-model",
        context_window: 128000,
        supports_vision: true,
      },
      {
        id: "opus-model",
        context_window: 200000,
        max_output_tokens: 100000,
        supports_vision: true,
      },
    ],
    { sonnet: "sonnet-model", haiku: "", opus: "opus-model" },
    85,
  );
  assert.deepEqual(conservative.environment, {
    CLAUDE_CODE_AUTO_COMPACT_WINDOW: "128000",
    CLAUDE_AUTOCOMPACT_PCT_OVERRIDE: "78",
  });
  assert.deepEqual(conservative.compaction, {
    contextWindow: 128000,
    compactAt: 99840,
    reserved: 28160,
    percent: 78,
    safeCompactAt: 100000,
    outputReserveLimited: true,
  });
});

test("Claude context environment keeps its final threshold within the exact output reserve", () => {
  const result = generator.claudeContextEnvironment(
    [
      {
        id: "boundary-model",
        context_window: 9007199254740991,
        max_output_tokens: 990791918021510,
        supports_vision: true,
      },
    ],
    { sonnet: "boundary-model", haiku: "", opus: "" },
    99,
  );

  assert.deepEqual(result.environment, {
    CLAUDE_CODE_AUTO_COMPACT_WINDOW: "9007199254740991",
    CLAUDE_AUTOCOMPACT_PCT_OVERRIDE: "88",
  });
  assert.deepEqual(result.compaction, {
    contextWindow: 9007199254740991,
    compactAt: 7926335344172072,
    reserved: 1080863910568919,
    percent: 88,
    safeCompactAt: 8016407336719481,
    outputReserveLimited: true,
  });
  assert.ok(
    result.compaction.compactAt <= result.compaction.safeCompactAt,
  );
});

test("Claude output reserve warning reflects the final aggregate threshold", () => {
  const result = generator.claudeContextEnvironment(
    [
      {
        id: "dominant-no-output",
        context_window: 100000,
        supports_vision: true,
      },
      {
        id: "non-dominant-capped",
        context_window: 1000000,
        max_output_tokens: 200000,
        supports_vision: true,
      },
    ],
    {
      sonnet: "dominant-no-output",
      haiku: "non-dominant-capped",
      opus: "",
    },
    85,
  );

  assert.equal(result.compaction.compactAt, 85000);
  assert.equal(result.compaction.outputReserveLimited, false);
});

test("Claude context environment omits both overrides for an exact-ID miss or missing context", () => {
  const profileModels = [
    {
      id: "Case-Sensitive",
      context_window: 128000,
      supports_vision: true,
    },
    { id: "missing-context", supports_vision: false },
  ];
  for (const mappings of [
    { sonnet: "case-sensitive", haiku: "", opus: "" },
    { sonnet: "Case-Sensitive ", haiku: "", opus: "" },
    { sonnet: "Case-Sensitive", haiku: "missing-context", opus: "" },
  ]) {
    const result = generator.claudeContextEnvironment(
      profileModels,
      mappings,
      85,
    );
    assert.deepEqual(result.environment, {});
    assert.equal(result.compaction, null);
    assert.match(result.warning, /Profile|上下文/);
  }

  const unusable = generator.claudeContextEnvironment(
    [{ id: "tiny", context_window: 1, supports_vision: false }],
    { sonnet: "tiny", haiku: "", opus: "" },
    1,
  );
  assert.deepEqual(unusable.environment, {});
  assert.equal(unusable.compaction, null);
  assert.match(unusable.warning, /省略|无效/);
});

test("Codex config uses only a Responses provider and an environment key", () => {
  assert.equal(typeof generator.codexConfig, "function");
  assert.equal(
    generator.codexConfig({
      baseURL: "https://proxy.example.com/coding/v1",
      model: "gpt-5.4",
      providerID: "llm_proxy_coding",
      providerName: "llm-proxy / Coding",
      envVariable: "OPENAI_API_KEY",
    }),
    [
      'model = "gpt-5.4"',
      'model_provider = "llm_proxy_coding"',
      "",
      '[model_providers."llm_proxy_coding"]',
      'name = "llm-proxy / Coding"',
      'base_url = "https://proxy.example.com/coding/v1"',
      'env_key = "OPENAI_API_KEY"',
      'wire_api = "responses"',
      "",
    ].join("\n"),
  );
});

test("Codex config emits numeric context limits before its Responses provider table", () => {
  const output = generator.codexConfig({
    baseURL: "https://proxy.example.com/coding/v1",
    model: "saved-model",
    providerID: "llm_proxy_coding",
    providerName: "llm-proxy / Coding",
    envVariable: "OPENAI_API_KEY",
    contextWindow: 128000,
    compactAt: 108800,
  });
  assert.match(
    output,
    /model_context_window = 128000\nmodel_auto_compact_token_limit = 108800\n\n\[model_providers\./,
  );
  assert.equal(output.includes('"128000"'), false);

  const withoutFacts = generator.codexConfig({
    baseURL: "https://proxy.example.com/coding/v1",
    model: "gpt-5.4",
    providerID: "llm_proxy_coding",
    providerName: "llm-proxy / Coding",
    envVariable: "",
  });
  assert.equal(withoutFacts.includes("model_context_window"), false);
  assert.equal(
    withoutFacts.includes("model_auto_compact_token_limit"),
    false,
  );
});

test("Codex config escapes TOML strings, validates env names, and can omit auth", () => {
  const output = generator.codexConfig({
    baseURL: "https://proxy.example.com/coding/v1",
    model: 'gpt"\ndanger = true',
    providerID: "llm_proxy_coding",
    providerName: 'Proxy"\nwire_api = "chat_completions',
    envVariable: "",
  });
  assert.equal(output.match(/^wire_api =/gm)?.length, 1);
  assert.equal(output.includes("\ndanger = true"), false);
  assert.equal(output.includes('\nwire_api = "chat_completions'), false);
  assert.equal(output.includes("env_key ="), false);
  assert.throws(
    () =>
      generator.codexConfig({
        baseURL: "https://proxy.example.com/coding/v1",
        model: "gpt-5.4",
        providerID: "llm_proxy_coding",
        providerName: "Coding",
        envVariable: "OPENAI_API_KEY\nheaders",
      }),
    /environment variable/i,
  );
});

test("Codex named profiles use current standalone files and safe names", () => {
  assert.equal(typeof generator.codexTarget, "function");
  assert.deepEqual(
    generator.codexTarget({ scope: "named", profileName: "coding-dev" }),
    {
      destination: "~/.codex/coding-dev.config.toml",
      filename: "coding-dev.config.toml",
      invocation: "codex --profile coding-dev",
    },
  );
  assert.throws(
    () =>
      generator.codexTarget({
        scope: "named",
        profileName: "../../config",
      }),
    /profile name/i,
  );
});

test("OpenCode configs select the native protocol package and env reference", () => {
  assert.equal(typeof generator.openCodeConfig, "function");
  const openAI = generator.openCodeConfig({
    protocol: "openai",
    baseURL: "https://proxy.example.com/openai/v1",
    providerID: "llm-proxy-openai",
    providerName: "llm-proxy / OpenAI",
    model: "gpt-5.4",
    envVariable: "OPENAI_API_KEY",
  });
  assert.deepEqual(openAI, {
    $schema: "https://opencode.ai/config.json",
    model: "llm-proxy-openai/gpt-5.4",
    provider: {
      "llm-proxy-openai": {
        npm: "@ai-sdk/openai",
        name: "llm-proxy / OpenAI",
        options: {
          baseURL: "https://proxy.example.com/openai/v1",
          apiKey: "{env:OPENAI_API_KEY}",
        },
        models: {
          "gpt-5.4": { name: "gpt-5.4" },
        },
      },
    },
  });

  const anthropic = generator.openCodeConfig({
    protocol: "anthropic",
    baseURL: "https://proxy.example.com/coding/v1",
    providerID: "llm-proxy-coding",
    providerName: "llm-proxy / Coding",
    model: "sonnet",
    envVariable: "ANTHROPIC_API_KEY",
  });
  assert.equal(
    anthropic.provider["llm-proxy-coding"].npm,
    "@ai-sdk/anthropic",
  );
  assert.equal(
    anthropic.provider["llm-proxy-coding"].options.apiKey,
    "{env:ANTHROPIC_API_KEY}",
  );

  const withoutAuth = generator.openCodeConfig({
    protocol: "openai",
    baseURL: "https://proxy.example.com/openai/v1",
    providerID: "llm-proxy-openai",
    providerName: "OpenAI",
    model: "gpt-5.4",
    envVariable: "",
  });
  assert.equal(
    Object.hasOwn(
      withoutAuth.provider["llm-proxy-openai"].options,
      "apiKey",
    ),
    false,
  );
  assert.throws(
    () =>
      generator.openCodeConfig({
        protocol: "openai",
        baseURL: "https://proxy.example.com/openai/v1",
        providerID: "llm-proxy-openai",
        providerName: "OpenAI",
        model: "gpt-5.4",
        envVariable: "OPENAI API KEY",
      }),
    /environment variable/i,
  );
});

test("OpenCode emits only stable model limits and top-level reserved compaction", () => {
  const config = generator.openCodeConfig({
    protocol: "openai",
    baseURL: "https://proxy.example.com/openai/v1",
    providerID: "llm-proxy-openai",
    providerName: "llm-proxy / OpenAI",
    model: "saved-model",
    envVariable: "OPENAI_API_KEY",
    contextWindow: 128000,
    maxOutputTokens: 16000,
    compactAt: 108800,
  });
  assert.deepEqual(
    config.provider["llm-proxy-openai"].models["saved-model"],
    {
      name: "saved-model",
      limit: { context: 128000, output: 16000 },
    },
  );
  assert.deepEqual(config.compaction, { auto: true, reserved: 19200 });
  assert.equal(JSON.stringify(config).includes("buffer"), false);
  assertStableOpenCodeModelLimit(
    config,
    "llm-proxy-openai",
    "saved-model",
  );

  const withoutFacts = generator.openCodeConfig({
    protocol: "openai",
    baseURL: "https://proxy.example.com/openai/v1",
    providerID: "llm-proxy-openai",
    providerName: "OpenAI",
    model: "gpt-5.4",
    envVariable: "",
  });
  assert.deepEqual(
    withoutFacts.provider["llm-proxy-openai"].models["gpt-5.4"],
    { name: "gpt-5.4" },
  );
  assert.equal(Object.hasOwn(withoutFacts, "compaction"), false);

  const contextOnly = generator.openCodeConfig({
    protocol: "openai",
    baseURL: "https://proxy.example.com/openai/v1",
    providerID: "llm-proxy-openai",
    providerName: "OpenAI",
    model: "GLM-5",
    envVariable: "",
    contextWindow: 204800,
    compactAt: 174080,
  });
  assert.deepEqual(
    contextOnly.provider["llm-proxy-openai"].models["GLM-5"],
    { name: "GLM-5" },
  );
  assert.equal(Object.hasOwn(contextOnly, "compaction"), false);
});

function assertStableOpenCodeModelLimit(config, providerID, modelID) {
  const limit = config.provider[providerID].models[modelID].limit;
  assert.equal(
    stableOpenCodeModelLimitSchema["x-source"],
    stableOpenCodeModelLimitSource,
  );
  assertMatchesJSONSchemaSubset(
    limit,
    stableOpenCodeModelLimitSchema,
  );
}

function assertMatchesJSONSchemaSubset(value, schema, path = "$") {
  if (schema.type === "number") {
    assert.equal(typeof value, "number", `${path} must be a number`);
    assert.equal(Number.isFinite(value), true, `${path} must be finite`);
    return;
  }
  assert.equal(schema.type, "object", `${path} schema type`);
  assert.equal(
    value !== null && typeof value === "object" && !Array.isArray(value),
    true,
    `${path} must be an object`,
  );
  for (const field of schema.required ?? []) {
    assert.equal(
      Object.hasOwn(value, field),
      true,
      `${path}.${field} is required`,
    );
  }
  if (schema.additionalProperties === false) {
    for (const field of Object.keys(value)) {
      assert.equal(
        Object.hasOwn(schema.properties ?? {}, field),
        true,
        `${path}.${field} is not allowed`,
      );
    }
  }
  for (const [field, propertySchema] of Object.entries(
    schema.properties ?? {},
  )) {
    if (Object.hasOwn(value, field)) {
      assertMatchesJSONSchemaSubset(
        value[field],
        propertySchema,
        `${path}.${field}`,
      );
    }
  }
}

test("OpenCode targets distinguish global and project downloads", () => {
  assert.equal(typeof generator.openCodeTarget, "function");
  assert.deepEqual(generator.openCodeTarget({ scope: "global" }), {
    destination: "~/.config/opencode/opencode.json",
    filename: "opencode.global.json",
  });
  assert.deepEqual(generator.openCodeTarget({ scope: "project" }), {
    destination: "opencode.json",
    filename: "opencode.json",
  });
  assert.throws(
    () => generator.openCodeTarget({ scope: "workspace" }),
    /opencode scope/i,
  );
});

test("Profile base URL normalizes the public origin and appends the slug once", () => {
  assert.equal(
    profileBaseURL("https://proxy.example.com/", "coding"),
    "https://proxy.example.com/coding",
  );
  assert.equal(
    profileBaseURL("https://proxy.example.com/coding?ignored=yes", "coding"),
    "https://proxy.example.com/coding",
  );
});

test("Profile base URL rejects credentials", () => {
  assert.throws(
    () => profileBaseURL("https://admin:secret@proxy.example.com", "coding"),
    /credentials/i,
  );
});

test("OpenAI API base includes v1 below the Profile prefix", () => {
  assert.equal(
    openAIBaseURL("https://proxy.example.com/", "openai"),
    "https://proxy.example.com/openai/v1",
  );
});

test("Claude settings include only non-blank aliases and a selected placeholder", () => {
  const output = claudeSettings({
    baseURL: "https://proxy.example.com/coding",
    models: { sonnet: " Kimi-K2.5 ", haiku: " ", opus: "GLM-5" },
    authVariable: "ANTHROPIC_AUTH_TOKEN",
  });

  assert.deepEqual(output, {
    $schema: "https://json.schemastore.org/claude-code-settings.json",
    env: {
      ANTHROPIC_BASE_URL: "https://proxy.example.com/coding",
      ANTHROPIC_DEFAULT_SONNET_MODEL: "Kimi-K2.5",
      ANTHROPIC_DEFAULT_OPUS_MODEL: "GLM-5",
      ANTHROPIC_AUTH_TOKEN: "<SET_LOCALLY>",
    },
  });
});

test("choosing no authentication omits every secret placeholder", () => {
  assert.deepEqual(
    claudeSettings({
      baseURL: "https://proxy.example.com/coding",
      models: {},
      authVariable: "不生成",
    }).env,
    { ANTHROPIC_BASE_URL: "https://proxy.example.com/coding" },
  );
});

test("OpenAI environment uses the v1 base without inventing a credential", () => {
  assert.deepEqual(
    openAIEnvironment({
      baseURL: "https://proxy.example.com/openai/v1",
      authVariable: "不生成",
    }),
    { OPENAI_BASE_URL: "https://proxy.example.com/openai/v1" },
  );
});

test("Shell output safely quotes single quotes", () => {
  assert.equal(
    shellSnippet({ ANTHROPIC_BASE_URL: "https://example.test/a'b" }),
    `export ANTHROPIC_BASE_URL='https://example.test/a'"'"'b'`,
  );
});

test("downloadText creates, clicks, and revokes a browser object URL", (t) => {
  const originalDocument = globalThis.document;
  const originalURL = globalThis.URL;
  const originalBlob = globalThis.Blob;
  const calls = [];
  const anchor = {
    href: "",
    download: "",
    click() {
      calls.push(["click", this.href, this.download]);
    },
  };

  globalThis.document = {
    createElement(name) {
      assert.equal(name, "a");
      return anchor;
    },
  };
  globalThis.Blob = class FakeBlob {
    constructor(parts, options) {
      this.parts = parts;
      this.options = options;
    }
  };
  globalThis.URL = {
    createObjectURL(blob) {
      calls.push(["create", blob.parts, blob.options]);
      return "blob:generator-test";
    },
    revokeObjectURL(url) {
      calls.push(["revoke", url]);
    },
  };
  t.after(() => {
    globalThis.document = originalDocument;
    globalThis.URL = originalURL;
    globalThis.Blob = originalBlob;
  });

  downloadText("settings.json", "{}", "application/json");

  assert.deepEqual(calls, [
    ["create", ["{}"], { type: "application/json" }],
    ["click", "blob:generator-test", "settings.json"],
    ["revoke", "blob:generator-test"],
  ]);
});

test("Anthropic generator exposes exact scopes and temporary placeholder inputs", async (t) => {
  const root = installFakeDOM(t);
  const listMarker = new FakeElement("p");
  listMarker.textContent = "Profiles remain mounted";
  root.append(listMarker);

  const dialog = openConfigurationGenerator(
    root,
    {
      slug: "coding",
      display_name: "Coding",
      config: { protocol: "anthropic" },
    },
    { origin: "https://proxy.example.com/_admin/profiles" },
  );

  assert.equal(dialog.open, true);
  assert.equal(root.children[0], listMarker);
  for (const scope of [
    "全局 ~/.claude/settings.json",
    "项目共享 .claude/settings.json",
    "项目私有 .claude/settings.local.json",
    "Shell",
  ]) {
    assert.ok(buttonByText(dialog, scope));
  }
  for (const name of [
    "generator-agent",
    "generator-public-origin",
    "generator-sonnet",
    "generator-haiku",
    "generator-opus",
    "generator-auth-variable",
  ]) {
    assert.ok(controlByName(dialog, name));
  }
  assert.deepEqual(
    controlByName(dialog, "generator-agent").children.map((option) => [
      option.value,
      option.textContent,
    ]),
    [
      ["claude-code", "Claude Code"],
      ["opencode-anthropic", "OpenCode (Anthropic)"],
      ["generic", "Generic"],
    ],
  );
  assert.deepEqual(
    controlByName(dialog, "generator-auth-variable").children.map(
      (option) => option.textContent,
    ),
    ["不生成", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"],
  );
  assert.equal(
    controls(dialog).some((control) => control.type === "password"),
    false,
  );

  const sonnet = controlByName(dialog, "generator-sonnet");
  sonnet.value = "kimi-k2.5";
  await sonnet.dispatch("input");
  const auth = controlByName(dialog, "generator-auth-variable");
  auth.value = "ANTHROPIC_AUTH_TOKEN";
  await auth.dispatch("change");
  assert.match(findClass(dialog, "generator-output").textContent, /kimi-k2\.5/);
  assert.match(
    findClass(dialog, "generator-output").textContent,
    /"<SET_LOCALLY>"/,
  );

  await buttonByText(dialog, "Shell").dispatch("click");
  assert.match(
    findClass(dialog, "generator-output").textContent,
    /export ANTHROPIC_BASE_URL='https:\/\/proxy\.example\.com\/coding'/,
  );
  assert.equal(buttonsByText(dialog, "复制").length, 1);
  assert.equal(buttonsByText(dialog, "下载").length, 1);

  await buttonByText(dialog, "返回 Profiles").dispatch("click");
  assert.equal(dialog.open, false);
  assert.deepEqual(root.children, [listMarker]);
});

test("Anthropic OpenCode adapter generates a native Messages config", async (t) => {
  const root = installFakeDOM(t);
  const dialog = openConfigurationGenerator(
    root,
    {
      slug: "coding",
      display_name: "Coding",
      config: { protocol: "anthropic" },
    },
    { origin: "https://proxy.example.com/" },
  );

  const agent = controlByName(dialog, "generator-agent");
  agent.value = "opencode-anthropic";
  await agent.dispatch("change");

  assert.ok(controlByName(dialog, "generator-model"));
  assert.ok(controlByName(dialog, "generator-env-variable"));
  const output = findClass(dialog, "generator-output").textContent;
  assert.match(output, /"npm": "@ai-sdk\/anthropic"/);
  assert.match(output, /"baseURL": "https:\/\/proxy\.example\.com\/coding\/v1"/);
  assert.match(output, /"apiKey": "\{env:ANTHROPIC_API_KEY\}"/);
  assert.ok(findText(dialog, "~/.config/opencode/opencode.json"));

  const scope = controlByName(dialog, "generator-opencode-scope");
  scope.value = "project";
  await scope.dispatch("change");
  assert.equal(findClass(dialog, "generator-output").textContent, output);
  assert.ok(findText(dialog, "目标：opencode.json"));
});

test("OpenAI generator shows prefix, v1 base, and generic Shell outputs", (t) => {
  const root = installFakeDOM(t);
  const dialog = openConfigurationGenerator(
    root,
    {
      slug: "openai",
      display_name: "OpenAI",
      config: { protocol: "openai" },
    },
    { origin: "https://proxy.example.com/" },
  );

  assert.ok(findText(dialog, "Profile 前缀"));
  assert.ok(findText(dialog, "https://proxy.example.com/openai"));
  assert.ok(findText(dialog, "OpenAI API base"));
  assert.ok(findText(dialog, "https://proxy.example.com/openai/v1"));
  assert.deepEqual(
    controlByName(dialog, "generator-agent").children.map((option) => [
      option.value,
      option.textContent,
    ]),
    [
      ["generic", "Generic"],
      ["opencode-responses", "OpenCode (Responses)"],
      ["codex-responses", "Codex (Responses)"],
    ],
  );
  assert.match(
    findClass(dialog, "generator-output").textContent,
    /OPENAI_BASE_URL/,
  );
  assert.equal(
    descendants(dialog).some((element) =>
      element.textContent.includes("Claude Code"),
    ),
    false,
  );
  assert.equal(buttonsByText(dialog, "复制").length, 3);
  assert.equal(buttonsByText(dialog, "下载").length, 3);
});

test("OpenAI adapters generate OpenCode and current Codex Responses configs", async (t) => {
  const root = installFakeDOM(t);
  const dialog = openConfigurationGenerator(
    root,
    {
      slug: "openai",
      display_name: "OpenAI",
      config: { protocol: "openai" },
    },
    { origin: "https://proxy.example.com/" },
  );
  const agent = controlByName(dialog, "generator-agent");

  agent.value = "opencode-responses";
  await agent.dispatch("change");
  assert.match(
    findClass(dialog, "generator-output").textContent,
    /"npm": "@ai-sdk\/openai"/,
  );
  assert.match(
    findClass(dialog, "generator-output").textContent,
    /"llm-proxy-openai"/,
  );
  assert.ok(findText(dialog, "上游必须支持 /v1/responses"));

  agent.value = "codex-responses";
  await agent.dispatch("change");
  assert.match(
    findClass(dialog, "generator-output").textContent,
    /wire_api = "responses"/,
  );
  assert.match(
    findClass(dialog, "generator-output").textContent,
    /model_provider = "llm_proxy_openai"/,
  );
  assert.ok(findText(dialog, "~/.codex/config.toml"));

  const scope = controlByName(dialog, "generator-codex-scope");
  scope.value = "named";
  await scope.dispatch("change");
  assert.ok(findText(dialog, "~/.codex/openai.config.toml"));
  assert.ok(findText(dialog, "codex --profile openai"));
  assert.equal(
    controls(dialog).some((control) => control.type === "password"),
    false,
  );
});

test("Agent panels use saved model facts for transient context settings and stable copy", async (t) => {
  const root = installFakeDOM(t);
  const profile = {
    slug: "openai",
    display_name: "OpenAI",
    config: {
      protocol: "openai",
      models: [
        {
          id: "Saved-Model",
          context_window: 128000,
          max_output_tokens: 16000,
          supports_vision: true,
        },
      ],
    },
  };
  const dialog = openConfigurationGenerator(
    root,
    profile,
    { origin: "https://proxy.example.com/" },
  );
  const agent = controlByName(dialog, "generator-agent");

  agent.value = "opencode-responses";
  await agent.dispatch("change");
  const openCodePercent = controlByName(
    dialog,
    "generator-compaction-percent",
  );
  assert.equal(openCodePercent.type, "number");
  assert.equal(openCodePercent.value, "85");
  assert.equal(openCodePercent.min, "1");
  assert.equal(openCodePercent.max, "99");
  assert.equal(openCodePercent.step, "1");
  assert.equal(controlByName(dialog, "generator-model").value, "Saved-Model");
  assert.ok(findText(dialog, "Saved-Model"));
  assert.match(
    findClass(dialog, "generator-output").textContent,
    /"limit": \{\s+"context": 128000,\s+"output": 16000/s,
  );
  assert.match(
    findClass(dialog, "generator-output").textContent,
    /"compaction": \{\s+"auto": true,\s+"reserved": 19200/s,
  );
  assert.equal(
    findClass(dialog, "generator-output").textContent.includes("buffer"),
    false,
  );
  assert.ok(findText(dialog, "有效阈值 108800"));

  agent.value = "codex-responses";
  await agent.dispatch("change");
  assert.equal(
    controlByName(dialog, "generator-compaction-percent").value,
    "85",
  );
  assert.equal(controlByName(dialog, "generator-model").value, "Saved-Model");
  assert.match(
    findClass(dialog, "generator-output").textContent,
    /model_context_window = 128000/,
  );
  assert.match(
    findClass(dialog, "generator-output").textContent,
    /model_auto_compact_token_limit = 108800/,
  );
  assert.ok(findText(dialog, "Codex 可能"));

  const model = controlByName(dialog, "generator-model");
  model.value = "saved-model";
  await model.dispatch("input");
  const missingOutput = findClass(dialog, "generator-output").textContent;
  assert.equal(missingOutput.includes("model_context_window"), false);
  const warning = findClass(dialog, "generator-capability-warning");
  assert.equal(warning.hidden, false);
  assert.match(warning.textContent, /Profile/);
});

test("OpenCode panel warns and omits schema-dependent fields for context-only models", async (t) => {
  const root = installFakeDOM(t);
  const dialog = openConfigurationGenerator(
    root,
    {
      slug: "openai",
      display_name: "OpenAI",
      config: {
        protocol: "openai",
        models: [
          {
            id: "GLM-5",
            context_window: 204800,
            supports_vision: false,
          },
        ],
      },
    },
    { origin: "https://proxy.example.com/" },
  );
  const agent = controlByName(dialog, "generator-agent");
  agent.value = "opencode-responses";
  await agent.dispatch("change");

  const output = findClass(dialog, "generator-output").textContent;
  assert.match(output, /"model": "llm-proxy-openai\/GLM-5"/);
  assert.equal(output.includes('"limit"'), false);
  assert.equal(output.includes('"compaction"'), false);
  const warning = findClass(dialog, "generator-capability-warning");
  assert.equal(warning.hidden, false);
  assert.match(warning.textContent, /OpenCode.*输出|输出.*OpenCode/);
});

test("OpenCode panel preserves its base config before compacting a one-token context-only model", async (t) => {
  const root = installFakeDOM(t);
  const dialog = openConfigurationGenerator(
    root,
    {
      slug: "openai",
      display_name: "OpenAI",
      config: {
        protocol: "openai",
        models: [
          {
            id: "tiny-context",
            context_window: 1,
            supports_vision: false,
          },
        ],
      },
    },
    { origin: "https://proxy.example.com/" },
  );
  const agent = controlByName(dialog, "generator-agent");
  agent.value = "opencode-responses";
  await agent.dispatch("change");

  const output = findClass(dialog, "generator-output").textContent;
  assert.match(output, /"model": "llm-proxy-openai\/tiny-context"/);
  assert.equal(output.includes('"limit"'), false);
  assert.equal(output.includes('"compaction"'), false);
  const warning = findClass(dialog, "generator-capability-warning");
  assert.equal(warning.hidden, false);
  assert.match(warning.textContent, /未.*最大输出|最大输出.*未/);
  const error = findClass(dialog, "error-banner");
  assert.equal(error.hidden, true);
  assert.equal(error.textContent, "");
});

test("Claude panel aggregates exact saved mappings and explains output reserve", async (t) => {
  const root = installFakeDOM(t);
  const dialog = openConfigurationGenerator(
    root,
    {
      slug: "coding",
      display_name: "Coding",
      config: {
        protocol: "anthropic",
        models: [
          {
            id: "Sonnet-Saved",
            context_window: 128000,
            supports_vision: true,
          },
          {
            id: "Opus-Saved",
            context_window: 200000,
            max_output_tokens: 100000,
            supports_vision: true,
          },
        ],
      },
    },
    { origin: "https://proxy.example.com/" },
  );
  assert.equal(
    controlByName(dialog, "generator-compaction-percent").value,
    "85",
  );
  const sonnet = controlByName(dialog, "generator-sonnet");
  const opus = controlByName(dialog, "generator-opus");
  sonnet.value = "Sonnet-Saved";
  opus.value = "Opus-Saved";
  await sonnet.dispatch("input");
  await opus.dispatch("input");

  const output = findClass(dialog, "generator-output").textContent;
  assert.match(output, /"CLAUDE_CODE_AUTO_COMPACT_WINDOW": "128000"/);
  assert.match(output, /"CLAUDE_AUTOCOMPACT_PCT_OVERRIDE": "78"/);
  assert.ok(findText(dialog, "有效阈值 99840"));
  assert.ok(findText(dialog, "最大输出"));
});

test("compaction percentage and model suggestions are dialog-local and reset on reopen", async (t) => {
  const root = installFakeDOM(t);
  const profile = {
    slug: "coding",
    display_name: "Coding",
    config: {
      protocol: "anthropic",
      models: [
        {
          id: "Profile-Model",
          context_window: 128000,
          supports_vision: true,
        },
      ],
    },
  };
  let dialog = openConfigurationGenerator(
    root,
    profile,
    { origin: "https://proxy.example.com/" },
  );
  const percent = controlByName(dialog, "generator-compaction-percent");
  percent.value = "72";
  await percent.dispatch("input");
  assert.deepEqual(profile.config.models, [
    {
      id: "Profile-Model",
      context_window: 128000,
      supports_vision: true,
    },
  ]);
  assert.ok(findText(dialog, "Profile-Model"));

  await buttonByText(dialog, "返回 Profiles").dispatch("click");
  dialog = openConfigurationGenerator(
    root,
    profile,
    { origin: "https://proxy.example.com/" },
  );
  assert.equal(
    controlByName(dialog, "generator-compaction-percent").value,
    "85",
  );
});

test("missing Profile models keep base configs while warning and never guessing context", async (t) => {
  const root = installFakeDOM(t);
  const dialog = openConfigurationGenerator(
    root,
    {
      slug: "openai",
      display_name: "OpenAI",
      config: { protocol: "openai", models: [] },
    },
    { origin: "https://proxy.example.com/" },
  );
  const agent = controlByName(dialog, "generator-agent");
  agent.value = "opencode-responses";
  await agent.dispatch("change");
  const openCodeOutput = findClass(dialog, "generator-output").textContent;
  assert.match(openCodeOutput, /"model": "llm-proxy-openai\/gpt-5\.4"/);
  assert.equal(openCodeOutput.includes('"limit"'), false);
  assert.equal(openCodeOutput.includes('"compaction"'), false);
  let warning = findClass(dialog, "generator-capability-warning");
  assert.equal(warning.hidden, false);
  assert.match(warning.textContent, /Profile/);

  agent.value = "codex-responses";
  await agent.dispatch("change");
  const codexOutput = findClass(dialog, "generator-output").textContent;
  assert.match(codexOutput, /model = "gpt-5\.4"/);
  assert.equal(codexOutput.includes("model_context_window"), false);
  warning = findClass(dialog, "generator-capability-warning");
  assert.equal(warning.hidden, false);
  assert.match(warning.textContent, /Profile/);
});

class FakeElement {
  constructor(tagName) {
    this.tagName = tagName.toUpperCase();
    this.children = [];
    this.parentNode = null;
    this.attributes = new Map();
    this.listeners = new Map();
    this.textContent = "";
    this.className = "";
    this.name = "";
    this.type = "";
    this.value = "";
    this.hidden = false;
    this.open = false;
  }

  set innerHTML(_) {
    throw new Error("innerHTML is forbidden");
  }

  append(...children) {
    for (const child of children) {
      child.parentNode = this;
      this.children.push(child);
    }
  }

  replaceChildren(...children) {
    for (const child of this.children) {
      child.parentNode = null;
    }
    this.children = [];
    this.append(...children);
  }

  remove() {
    if (!this.parentNode) {
      return;
    }
    this.parentNode.children = this.parentNode.children.filter(
      (child) => child !== this,
    );
    this.parentNode = null;
  }

  setAttribute(name, value) {
    this.attributes.set(name, String(value));
  }

  getAttribute(name) {
    return this.attributes.get(name) ?? null;
  }

  addEventListener(name, listener) {
    const listeners = this.listeners.get(name) ?? [];
    listeners.push(listener);
    this.listeners.set(name, listeners);
  }

  async dispatch(name) {
    const event = {
      target: this,
      currentTarget: this,
      preventDefault() {},
    };
    for (const listener of this.listeners.get(name) ?? []) {
      await listener(event);
    }
  }

  click() {}

  showModal() {
    this.open = true;
  }

  close() {
    this.open = false;
  }
}

function installFakeDOM(t) {
  const root = new FakeElement("main");
  const originalDocument = globalThis.document;
  const originalLocalStorage = globalThis.localStorage;
  const originalSessionStorage = globalThis.sessionStorage;
  globalThis.document = {
    createElement: (name) => new FakeElement(name),
  };
  Object.defineProperty(globalThis, "localStorage", {
    configurable: true,
    get() {
      throw new Error("localStorage access is forbidden");
    },
  });
  Object.defineProperty(globalThis, "sessionStorage", {
    configurable: true,
    get() {
      throw new Error("sessionStorage access is forbidden");
    },
  });
  t.after(() => {
    globalThis.document = originalDocument;
    restoreGlobal("localStorage", originalLocalStorage);
    restoreGlobal("sessionStorage", originalSessionStorage);
  });
  return root;
}

function restoreGlobal(name, value) {
  if (value === undefined) {
    delete globalThis[name];
    return;
  }
  Object.defineProperty(globalThis, name, {
    configurable: true,
    value,
  });
}

function descendants(root) {
  return [root, ...root.children.flatMap(descendants)];
}

function controls(root) {
  return descendants(root).filter((element) =>
    ["INPUT", "SELECT", "TEXTAREA"].includes(element.tagName),
  );
}

function controlByName(root, name) {
  const control = controls(root).find((element) => element.name === name);
  assert.ok(control, `control ${name} not found`);
  return control;
}

function findText(root, text) {
  const found = descendants(root).find((element) =>
    element.textContent.includes(text),
  );
  assert.ok(found, `text ${text} not found`);
  return found;
}

function findClass(root, className) {
  const found = descendants(root).find((element) =>
    element.className.split(/\s+/).includes(className),
  );
  assert.ok(found, `class ${className} not found`);
  return found;
}

function buttonsByText(root, text) {
  return descendants(root).filter(
    (element) => element.tagName === "BUTTON" && element.textContent === text,
  );
}

function buttonByText(root, text) {
  const button = buttonsByText(root, text)[0];
  assert.ok(button, `button ${text} not found`);
  return button;
}
