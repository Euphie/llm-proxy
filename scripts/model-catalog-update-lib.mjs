import {
  createHash,
  randomUUID as systemRandomUUID,
} from "node:crypto";
import {
  open,
  rename,
  rm,
} from "node:fs/promises";

const DEFAULT_MODELS_URL = "https://models.dev/models.json";
const DEFAULT_PROVIDERS_URL = "https://models.dev/api.json";

export async function fetchModelsDevCatalogInputs({
  fetchImpl = globalThis.fetch,
  modelsUrl = DEFAULT_MODELS_URL,
  providersUrl = DEFAULT_PROVIDERS_URL,
  retrieved = new Date().toISOString().slice(0, 10),
} = {}) {
  const [modelsResponse, providersResponse] = await Promise.all([
    fetchImpl(modelsUrl),
    fetchImpl(providersUrl),
  ]);
  const [modelsContents, providersContents] = await Promise.all([
    readFeed(modelsResponse, modelsUrl),
    readFeed(providersResponse, providersUrl),
  ]);
  const modelsSha256 = sha256(modelsContents);
  const providersSha256 = sha256(providersContents);
  const revision = sha256(
    Buffer.from(
      "models.dev/catalog/v1\0" +
        `models.json\0${modelsSha256}\0` +
        `api.json\0${providersSha256}\0`,
      "utf8",
    ),
  );

  return {
    canonical: parseFeed(modelsContents, modelsUrl),
    providers: parseFeed(providersContents, providersUrl),
    source: {
      name: "Models.dev",
      revision,
      modelsSha256,
      providersSha256,
      retrieved,
    },
  };
}

export function makeTemporaryPath(
  outputPath,
  {
    pid = process.pid,
    randomUUID = systemRandomUUID,
  } = {},
) {
  return `${outputPath}.${pid}.${randomUUID()}.tmp`;
}

export async function atomicReplaceFile(
  outputPath,
  contents,
  options = {},
) {
  const temporaryPath = makeTemporaryPath(outputPath, options);
  const openFile = options.openFile ?? open;
  let handle;
  let created = false;
  try {
    handle = await openFile(temporaryPath, "wx");
    created = true;
    await handle.writeFile(contents, { encoding: "utf8" });
    await handle.close();
    handle = undefined;
    await rename(temporaryPath, outputPath);
  } catch (error) {
    if (handle) {
      await handle.close().catch(() => {});
    }
    if (created) {
      await rm(temporaryPath, { force: true });
    }
    throw error;
  }
}

async function readFeed(response, url) {
  if (!response.ok) {
    throw new Error(
      `failed to fetch ${url}: ${response.status} ${response.statusText}`,
    );
  }
  return Buffer.from(await response.arrayBuffer());
}

function parseFeed(contents, url) {
  try {
    return JSON.parse(contents.toString("utf8"));
  } catch (error) {
    throw new SyntaxError(`failed to parse ${url}: ${error.message}`, {
      cause: error,
    });
  }
}

function sha256(contents) {
  return createHash("sha256").update(contents).digest("hex");
}
