import assert from "node:assert/strict";
import test from "node:test";

import {
  buildCatalog,
  renderCatalogModule,
} from "./model-catalog-lib.mjs";

const sourceFixture = {
  name: "Models.dev",
  revision:
    "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
  modelsSha256:
    "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
  providersSha256:
    "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
  retrieved: "2026-07-30",
};

const canonicalFixture = {
  "google/gemini-preview": {
    name: "Gemini Preview",
    family: "gemini",
    tool_call: true,
    release_date: "2026-07-01",
    last_updated: "2026-07-02",
    modalities: {
      input: ["text", "image"],
      output: ["text"],
    },
    limit: { context: 128000 },
  },
  "zhipuai/glm-5.2": {
    name: "GLM-5.2",
    family: "glm",
    tool_call: true,
    release_date: "2026-06-13",
    last_updated: "2026-06-13",
    modalities: {
      input: ["text"],
      output: ["text"],
    },
    limit: {
      context: 1000000,
      output: 131072,
    },
  },
  "legacy/deprecated-only": {
    name: "Deprecated Only",
    family: "legacy",
    tool_call: true,
    modalities: {
      input: ["text"],
      output: ["text"],
    },
    limit: { context: 4096, output: 1024 },
  },
  "acme/stable-vision-without-tools": {
    name: "Stable Vision Without Tools",
    family: "acme",
    tool_call: false,
    modalities: {
      input: ["text", "image"],
      output: ["text"],
    },
    limit: { context: 8192, output: 2048 },
  },
};

const providerFixture = {
  google: {
    name: "Google",
    models: {
      "gemini-preview": {
        id: "gemini-preview",
        status: "beta",
      },
    },
  },
  zhipuai: {
    name: "Zhipu AI",
    models: {
      "glm-5.2": {
        id: "glm-5.2",
      },
    },
  },
  legacy: {
    name: "Legacy Inc.",
    models: {
      "deprecated-only": {
        id: "deprecated-only",
        status: "deprecated",
      },
    },
  },
  acme: {
    name: "Acme",
    models: {
      "stable-vision-without-tools": {
        id: "stable-vision-without-tools",
      },
    },
  },
};

const overridesFixture = {
  "zhipuai/glm-5.2": {
    compatibilityAliases: ["claude-glm-5.2"],
    sourceUrl:
      "https://docs.bigmodel.cn/cn/guide/models/text/glm-5.2",
  },
};

test("buildCatalog filters ineligible records and compacts confirmed values", () => {
  const models = buildFixture();

  assert.deepEqual(models.map(({ canonicalId }) => canonicalId), [
    "google/gemini-preview",
    "zhipuai/glm-5.2",
  ]);
  assert.equal(models[0].lifecycle, "preview");
  assert.equal(models[0].supports_vision, true);
  assert.equal(
    Object.hasOwn(models[0], "max_output_tokens"),
    false,
    "unknown output limits must stay absent",
  );
  assert.deepEqual(models[1], {
    id: "glm-5.2",
    canonicalId: "zhipuai/glm-5.2",
    apiIds: ["glm-5.2"],
    aliases: ["zhipuai/glm-5.2"],
    compatibilityAliases: ["claude-glm-5.2"],
    provider: "Zhipu AI",
    name: "GLM-5.2",
    family: "glm",
    context_window: 1000000,
    max_output_tokens: 131072,
    supports_vision: false,
    lifecycle: "stable",
    releaseDate: "2026-06-13",
    lastUpdated: "2026-06-13",
    references: [{
      name: "Z.AI GLM-5.2",
      url: "https://docs.bigmodel.cn/cn/guide/models/text/glm-5.2",
    }],
    source: sourceFixture,
  });
});

test("buildCatalog does not treat audio input as vision support", () => {
  const models = buildCatalog({
    canonical: {
      "nvidia/nemotron-voicechat": {
        name: "Nemotron VoiceChat",
        family: "nemotron",
        tool_call: true,
        modalities: {
          input: ["text", "audio"],
          output: ["text"],
        },
        limit: { context: 32768, output: 4096 },
      },
    },
    providers: {
      nvidia: {
        name: "NVIDIA",
        models: {
          "nemotron-voicechat": {
            id: "nemotron-voicechat",
          },
        },
      },
    },
    overrides: {},
    source: sourceFixture,
  });

  assert.equal(models.length, 1);
  assert.equal(models[0].supports_vision, false);
});

test("buildCatalog omits an output limit equal to the context window", () => {
  const models = buildCatalog({
    canonical: {
      "equal/model": {
        name: "Equal Limit",
        family: "equal",
        tool_call: true,
        modalities: { input: ["text"], output: ["text"] },
        limit: { context: 8192, output: 8192 },
      },
    },
    providers: {
      equal: {
        name: "Equal",
        models: { model: { id: "model" } },
      },
    },
    overrides: {},
    source: sourceFixture,
  });

  assert.equal(models[0].context_window, 8192);
  assert.equal(Object.hasOwn(models[0], "max_output_tokens"), false);
});

test("buildCatalog rejects output limits greater than context", () => {
  assert.throws(
    () => buildCatalog({
      canonical: {
        "broken/model": {
          name: "Broken",
          family: "broken",
          tool_call: true,
          modalities: { input: ["text"], output: ["text"] },
          limit: { context: 8192, output: 8193 },
        },
      },
      providers: {
        broken: {
          name: "Broken",
          models: { model: { id: "model" } },
        },
      },
      overrides: {},
      source: sourceFixture,
    }),
    /output.*context/i,
  );
});

test("buildCatalog rejects non-positive and unsafe token limits", () => {
  for (const [field, value] of [
    ["context", 0],
    ["context", -1],
    ["context", Number.MAX_SAFE_INTEGER + 1],
    ["output", 0],
    ["output", -1],
    ["output", Number.MAX_SAFE_INTEGER + 1],
  ]) {
    assert.throws(
      () => buildCatalog({
        canonical: {
          "broken/model": {
            name: "Broken",
            family: "broken",
            tool_call: true,
            modalities: { input: ["text"], output: ["text"] },
            limit: {
              context: field === "context" ? value : 8192,
              output: field === "output" ? value : 1024,
            },
          },
        },
        providers: {
          broken: {
            name: "Broken",
            models: { model: { id: "model" } },
          },
        },
        overrides: {},
        source: sourceFixture,
      }),
      /positive safe integer/i,
      `${field}=${value}`,
    );
  }
});

test("buildCatalog rejects duplicate exact aliases", () => {
  assert.throws(
    () => buildCatalog({
      canonical: {
        "one/model-one": canonicalTextModel("One"),
        "two/model-two": canonicalTextModel("Two"),
      },
      providers: {
        one: {
          name: "One",
          models: { "model-one": { id: "one-model" } },
        },
        two: {
          name: "Two",
          models: { "model-two": { id: "two-model" } },
        },
      },
      overrides: {
        "one/model-one": { aliases: ["shared-alias"] },
        "two/model-two": { aliases: ["shared-alias"] },
      },
      source: sourceFixture,
    }),
    /duplicate.*alias.*shared-alias/i,
  );
});

test("buildCatalog rejects duplicate exact model and API identifiers", () => {
  assert.throws(
    () => buildCatalog({
      canonical: {
        "one/model": canonicalTextModel("One"),
        "two/model": canonicalTextModel("Two"),
      },
      providers: {
        one: {
          name: "One",
          models: { model: { id: "shared" } },
        },
        two: {
          name: "Two",
          models: { model: { id: "SHARED" } },
        },
      },
      overrides: {},
      source: sourceFixture,
    }),
    /duplicate exact identifier.*shared/i,
  );
});

test("buildCatalog rejects identifiers with surrounding whitespace", () => {
  assert.throws(
    () => buildCatalog({
      canonical: {
        "one/model": canonicalTextModel("One"),
      },
      providers: {
        one: {
          name: "One",
          models: { model: { id: "model" } },
        },
      },
      overrides: {
        "one/model": {
          compatibilityAliases: [" claude-model"],
        },
      },
      source: sourceFixture,
    }),
    /identifier.*surrounding whitespace/i,
  );
});

test("an active provider entry keeps a canonical model marked deprecated", () => {
  const models = buildCatalog({
    canonical: {
      "origin/model": {
        ...canonicalTextModel("Mixed Status"),
        status: "deprecated",
      },
    },
    providers: {
      archived: {
        name: "Archived",
        models: {
          "origin/model": {
            id: "archived-model",
            status: "deprecated",
          },
        },
      },
      proxy: {
        name: "Proxy",
        models: {
          "origin/model": {
            id: "proxy-model",
          },
        },
      },
    },
    overrides: {},
    source: sourceFixture,
  });

  assert.equal(models.length, 1);
  assert.equal(models[0].lifecycle, "stable");
  assert.deepEqual(models[0].apiIds, [
    "archived-model",
    "proxy-model",
  ]);
});

test("buildCatalog requires complete source metadata", () => {
  for (const missing of [
    "revision",
    "modelsSha256",
    "providersSha256",
  ]) {
    const source = { ...sourceFixture };
    delete source[missing];

    assert.throws(
      () => buildCatalog({
        canonical: canonicalFixture,
        providers: providerFixture,
        overrides: overridesFixture,
        source,
      }),
      new RegExp(`source.*${missing}`, "i"),
      missing,
    );
  }
});

test("buildCatalog rejects malformed source digests", () => {
  for (const field of [
    "revision",
    "modelsSha256",
    "providersSha256",
  ]) {
    assert.throws(
      () => buildCatalog({
        canonical: canonicalFixture,
        providers: providerFixture,
        overrides: overridesFixture,
        source: {
          ...sourceFixture,
          [field]: "not-a-sha256",
        },
      }),
      new RegExp(`source.*${field}.*SHA-256`, "i"),
      field,
    );
  }
});

test("renderCatalogModule is deterministic", () => {
  const models = buildFixture();
  const first = renderCatalogModule({ models, source: sourceFixture });
  const second = renderCatalogModule({ models, source: sourceFixture });

  assert.equal(first, second);
  assert.match(first, /export const MODELS_DEV_SOURCE/);
  assert.match(first, /export const BUILT_IN_MODELS/);
});

function buildFixture() {
  return buildCatalog({
    canonical: canonicalFixture,
    providers: providerFixture,
    overrides: overridesFixture,
    source: sourceFixture,
  });
}

function canonicalTextModel(name) {
  return {
    name,
    family: "fixture",
    tool_call: true,
    modalities: { input: ["text"], output: ["text"] },
    limit: { context: 8192, output: 1024 },
  };
}
