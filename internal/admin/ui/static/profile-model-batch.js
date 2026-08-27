import {
  applyModelSuggestion,
  matchModelSuggestions,
} from "./model-catalog.js";

const maximumBatchIDs = 100;

export function previewBatchModels(
  text,
  existingModels = [],
  matcher = matchModelSuggestions,
) {
  const existing = new Set(existingModels.map((model) => String(model.id ?? "")));
  const seen = new Set();
  let uniqueCount = 0;
  const rows = [];

  for (const line of String(text ?? "").split(/\r?\n/)) {
    const id = line.trim();
    if (!id) {
      continue;
    }
    if (seen.has(id)) {
      rows.push({ id, status: "duplicate", candidates: [] });
      continue;
    }
    seen.add(id);
    uniqueCount++;
    if (uniqueCount > maximumBatchIDs) {
      throw new RangeError(`单次最多录入 ${maximumBatchIDs} 个不同模型 ID。`);
    }
    if (existing.has(id)) {
      rows.push({ id, status: "existing", candidates: [] });
      continue;
    }

    const candidates = matcher(id, { limit: 5 });
    if (candidates.length === 1 && candidates[0].autoApply) {
      rows.push({
        id,
        status: "ready",
        candidates,
        selectedMatch: candidates[0],
        model: {
          ...applyModelSuggestion({ id }, candidates[0]),
          canonical_model_id:
            candidates[0].entry.canonicalId || candidates[0].entry.id,
        },
      });
      continue;
    }
    if (candidates.length > 0) {
      rows.push({ id, status: "choose", candidates });
      continue;
    }
    rows.push({ id, status: "unknown", candidates: [], model: { id } });
  }
  return rows;
}

export function importBatchModels(rows) {
  return (rows || [])
    .filter((row) => !["duplicate", "existing"].includes(row.status))
    .map((row) => structuredClone(row.model || { id: row.id }));
}
