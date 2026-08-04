import { createModelCatalogMatcher } from "./model-catalog.js";
import { modelReferences } from "./profile-readiness.js";
import { profileSectionHref } from "./routes.js";

const comparedFields = [
  ["context_window", "上下文窗口"],
  ["max_output_tokens", "最大输出 Token"],
  ["supports_vision", "视觉能力"],
  ["supports_tools", "工具调用能力"],
  ["supports_structured_output", "结构化输出能力"],
  ["input_price_micro_usd_per_million", "输入价格"],
  ["output_price_micro_usd_per_million", "输出价格"],
  ["cache_read_price_micro_usd_per_million", "缓存读取价格"],
  ["cache_write_price_micro_usd_per_million", "缓存写入价格"],
];

export function analyzeCatalogImpact({
  previous = { models: [] },
  current = { models: [] },
  profiles = [],
  strategiesByProfile = {},
} = {}) {
  const changes = catalogChanges(previous.models || [], current.models || []);
  if (changes.size === 0) {
    return [];
  }
  const previousMatcher = createModelCatalogMatcher(previous.models || []);
  const currentMatcher = createModelCatalogMatcher(current.models || []);
  const rows = [];
  const seen = new Set();

  const add = (row) => {
    const key = [
      row.profileId,
      row.modelId,
      row.section,
      row.label,
      row.strategy?.id || "",
      row.strategy?.state || "",
    ].join("\0");
    if (!seen.has(key)) {
      seen.add(key);
      rows.push(row);
    }
  };

  for (const profile of profiles) {
    for (const saved of profile.config?.models || []) {
      const canonicalID = matchedCanonicalID(
        saved.id,
        currentMatcher,
        previousMatcher,
      );
      const change = changes.get(canonicalID);
      if (!change) {
        continue;
      }
      const base = {
        profileId: Number(profile.id),
        profileName: profile.display_name || profile.slug,
        modelId: String(saved.id),
        canonicalId: canonicalID,
        changes: change.fields,
        risk: change.risk,
      };
      add({
        ...base,
        section: "models",
        label: "模型参数",
        strategy: null,
        href: profileSectionHref(profile.id, "models"),
      });
      for (const reference of modelReferences(profile, saved.id)) {
        add({
          ...base,
          section: reference.section,
          label: reference.label,
          strategy: null,
          href: profileSectionHref(profile.id, reference.section),
        });
      }

      const overview = strategiesForProfile(strategiesByProfile, profile.id);
      for (const version of overview?.strategies || []) {
        for (const route of version.config?.routes || []) {
          if (!(route.candidates || []).some((candidate) => candidate.model === saved.id)) {
            continue;
          }
          add({
            ...base,
            section: "routing",
            label: `Route ${route.id || "未命名"}`,
            strategy: {
              id: Number(version.id),
              state: String(version.state || ""),
              name: String(version.config?.name || version.id || ""),
            },
            href: profileSectionHref(profile.id, "routing"),
          });
        }
      }
    }
  }

  const riskOrder = { high: 0, medium: 1, low: 2 };
  return rows.sort((left, right) =>
    (riskOrder[left.risk] - riskOrder[right.risk]) ||
    left.profileName.localeCompare(right.profileName) ||
    left.modelId.localeCompare(right.modelId) ||
    left.label.localeCompare(right.label)
  );
}

function catalogChanges(previousModels, currentModels) {
  const previous = new Map(previousModels.map((model) => [model.canonicalId, model]));
  const current = new Map(currentModels.map((model) => [model.canonicalId, model]));
  const ids = new Set([...previous.keys(), ...current.keys()]);
  const changes = new Map();
  for (const id of ids) {
    const before = previous.get(id);
    const after = current.get(id);
    const fields = comparedFields
      .filter(([field]) => before?.[field] !== after?.[field])
      .map(([field, label]) => ({
        field,
        label,
        previous: before?.[field],
        current: after?.[field],
      }));
    if (fields.length === 0) {
      continue;
    }
    changes.set(id, { fields, risk: impactRisk(fields) });
  }
  return changes;
}

function impactRisk(fields) {
  for (const change of fields) {
    if (
      change.field.startsWith("supports_") &&
      change.previous === true &&
      change.current !== true
    ) {
      return "high";
    }
    if (
      (change.field === "context_window" || change.field === "max_output_tokens") &&
      typeof change.previous === "number" &&
      (typeof change.current !== "number" || change.current < change.previous)
    ) {
      return "high";
    }
  }
  if (fields.some((change) => change.field.includes("price_"))) {
    return "medium";
  }
  return "low";
}

function matchedCanonicalID(id, currentMatcher, previousMatcher) {
  for (const matcher of [currentMatcher, previousMatcher]) {
    const candidates = matcher.matchModelSuggestions(id, { limit: 2 });
    if (candidates.length === 1) {
      return candidates[0].entry.canonicalId;
    }
  }
  return "";
}

function strategiesForProfile(values, profileID) {
  if (values instanceof Map) {
    return values.get(Number(profileID)) || values.get(String(profileID));
  }
  return values?.[profileID] || values?.[String(profileID)];
}
