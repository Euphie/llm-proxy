import assert from "node:assert/strict";
import {
  mkdtemp,
  mkdir,
  open,
  readFile,
  readdir,
  rm,
  writeFile,
} from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import * as updateCatalog from "./model-catalog-update-lib.mjs";

const {
  atomicReplaceFile,
  makeTemporaryPath,
} = updateCatalog;

test("fetchModelsDevCatalogInputs identifies the exact feed contents", async () => {
  const modelsContents =
    "{\"openai/gpt\":{\"name\":\"GPT\"}}\n";
  const providersContents =
    "{\"openai\":{\"models\":{}}}\n";
  const requests = [];

  const result = await updateCatalog.fetchModelsDevCatalogInputs({
    fetchImpl: async (url) => {
      requests.push(url);
      if (url === "https://fixture.test/models.json") {
        return new Response(modelsContents);
      }
      if (url === "https://fixture.test/api.json") {
        return new Response(providersContents);
      }
      return new Response("not found", {
        status: 404,
        statusText: "Not Found",
      });
    },
    modelsUrl: "https://fixture.test/models.json",
    providersUrl: "https://fixture.test/api.json",
    retrieved: "2026-07-30",
  });

  assert.deepEqual(requests.sort(), [
    "https://fixture.test/api.json",
    "https://fixture.test/models.json",
  ]);
  assert.deepEqual(result.canonical, {
    "openai/gpt": { name: "GPT" },
  });
  assert.deepEqual(result.providers, {
    openai: { models: {} },
  });
  assert.deepEqual(result.source, {
    name: "Models.dev",
    revision:
      "2783740f464080f856586b2bfb646f415a3d1681772749cf319017938364868b",
    modelsSha256:
      "d2c5cf531ddc7b070c3ce9c0668a4fbddf273a12d0d1a109abfbed2b3bfa580a",
    providersSha256:
      "a8102f2916f6b46f849f5fa1ac93389ceeb32b2c93c53a97ebc0970f7e28e9d8",
    retrieved: "2026-07-30",
  });
});

test("feed revision changes when only JSON formatting changes", async () => {
  const compact = await updateCatalog.fetchModelsDevCatalogInputs({
    fetchImpl: fixtureFeedFetch("{}", "{}"),
    modelsUrl: "https://fixture.test/models.json",
    providersUrl: "https://fixture.test/api.json",
    retrieved: "2026-07-30",
  });
  const formatted = await updateCatalog.fetchModelsDevCatalogInputs({
    fetchImpl: fixtureFeedFetch("{}\n", "{}"),
    modelsUrl: "https://fixture.test/models.json",
    providersUrl: "https://fixture.test/api.json",
    retrieved: "2026-07-30",
  });

  assert.notEqual(
    compact.source.modelsSha256,
    formatted.source.modelsSha256,
  );
  assert.notEqual(compact.source.revision, formatted.source.revision);
});

test("fetchModelsDevCatalogInputs reports a failed feed response", async () => {
  await assert.rejects(
    updateCatalog.fetchModelsDevCatalogInputs({
      fetchImpl: async (url) =>
        url.endsWith("/models.json")
          ? new Response("upstream unavailable", {
            status: 503,
            statusText: "Service Unavailable",
          })
          : new Response("{}"),
      modelsUrl: "https://fixture.test/models.json",
      providersUrl: "https://fixture.test/api.json",
      retrieved: "2026-07-30",
    }),
    /models\.json: 503 Service Unavailable/,
  );
});

test("makeTemporaryPath creates unique sibling paths", () => {
  const outputPath = "/tmp/model-catalog-data.js";
  const first = makeTemporaryPath(outputPath, {
    pid: 42,
    randomUUID: () => "first",
  });
  const second = makeTemporaryPath(outputPath, {
    pid: 42,
    randomUUID: () => "second",
  });

  assert.equal(first, "/tmp/model-catalog-data.js.42.first.tmp");
  assert.equal(second, "/tmp/model-catalog-data.js.42.second.tmp");
  assert.notEqual(first, second);
});

test("atomicReplaceFile supports concurrent writers without shared temp files", async () => {
  await withTemporaryDirectory(async (directory) => {
    const outputPath = join(directory, "model-catalog-data.js");
    await writeFile(outputPath, "old", "utf8");

    await Promise.all([
      atomicReplaceFile(outputPath, "first", {
        pid: 1,
        randomUUID: () => "first",
      }),
      atomicReplaceFile(outputPath, "second", {
        pid: 2,
        randomUUID: () => "second",
      }),
    ]);

    assert.ok(
      new Set(["first", "second"]).has(
        await readFile(outputPath, "utf8"),
      ),
    );
    assert.deepEqual(await readdir(directory), ["model-catalog-data.js"]);
  });
});

test("atomicReplaceFile cleans only its own temp after rename failure", async () => {
  await withTemporaryDirectory(async (directory) => {
    const outputPath = join(directory, "model-catalog-data.js");
    await mkdir(outputPath);
    const foreignPath = `${outputPath}.foreign.tmp`;
    await writeFile(foreignPath, "foreign", "utf8");

    await assert.rejects(
      atomicReplaceFile(outputPath, "new", {
        pid: 7,
        randomUUID: () => "owned",
      }),
    );

    assert.equal(await readFile(foreignPath, "utf8"), "foreign");
    assert.deepEqual(
      (await readdir(directory)).sort(),
      ["model-catalog-data.js", "model-catalog-data.js.foreign.tmp"],
    );
  });
});

test("atomicReplaceFile uses exclusive creation without deleting a collision", async () => {
  await withTemporaryDirectory(async (directory) => {
    const outputPath = join(directory, "model-catalog-data.js");
    const temporaryPath = makeTemporaryPath(outputPath, {
      pid: 9,
      randomUUID: () => "collision",
    });
    await writeFile(temporaryPath, "foreign", "utf8");

    await assert.rejects(
      atomicReplaceFile(outputPath, "new", {
        pid: 9,
        randomUUID: () => "collision",
      }),
      { code: "EEXIST" },
    );

    assert.equal(await readFile(temporaryPath, "utf8"), "foreign");
  });
});

test("atomicReplaceFile removes an owned partial temp after write failure", async () => {
  await withTemporaryDirectory(async (directory) => {
    const outputPath = join(directory, "model-catalog-data.js");
    await writeFile(outputPath, "old", "utf8");

    await assert.rejects(
      atomicReplaceFile(outputPath, "replacement", {
        pid: 10,
        randomUUID: () => "partial",
        openFile: async (path, flags) => {
          const handle = await open(path, flags);
          return {
            async writeFile(contents, options) {
              await handle.writeFile(contents.slice(0, 2), options);
              throw new Error("simulated write failure");
            },
            close() {
              return handle.close();
            },
          };
        },
      }),
      /simulated write failure/,
    );

    assert.equal(await readFile(outputPath, "utf8"), "old");
    assert.deepEqual(await readdir(directory), ["model-catalog-data.js"]);
  });
});

async function withTemporaryDirectory(run) {
  const directory = await mkdtemp(
    join(tmpdir(), "model-catalog-update-test-"),
  );
  try {
    await run(directory);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
}

function fixtureFeedFetch(modelsContents, providersContents) {
  return async (url) =>
    new Response(
      url.endsWith("/models.json")
        ? modelsContents
        : providersContents,
    );
}
