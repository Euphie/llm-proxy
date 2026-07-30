import { fileURLToPath } from "node:url";

import {
  buildCatalog,
  renderCatalogModule,
} from "./model-catalog-lib.mjs";
import { MODEL_CATALOG_OVERRIDES } from "./model-catalog-overrides.mjs";
import {
  atomicReplaceFile,
  fetchModelsDevCatalogInputs,
} from "./model-catalog-update-lib.mjs";

const outputPath = fileURLToPath(new URL(
  "../internal/admin/ui/static/model-catalog-data.js",
  import.meta.url,
));

const { canonical, providers, source } =
  await fetchModelsDevCatalogInputs();
const models = buildCatalog({
  canonical,
  providers,
  overrides: MODEL_CATALOG_OVERRIDES,
  source,
});
const moduleSource = renderCatalogModule({ models, source });

await atomicReplaceFile(outputPath, moduleSource);
console.log(
  `wrote ${models.length} models from Models.dev ${source.revision}`,
);
