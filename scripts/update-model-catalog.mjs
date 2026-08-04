import { fileURLToPath } from "node:url";

import {
  buildCatalog,
  renderCatalogJSON,
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
const runtimeOutputPath = fileURLToPath(new URL(
  "../internal/modelcatalog/data/catalog.json",
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
const runtimeSource = renderCatalogJSON({ models, source });

await atomicReplaceFile(outputPath, moduleSource);
await atomicReplaceFile(runtimeOutputPath, runtimeSource);
console.log(
  `wrote ${models.length} models from Models.dev ${source.revision} to browser and runtime catalogs`,
);
