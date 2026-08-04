import {
  BUILT_IN_MODELS,
  MODELS_DEV_SOURCE,
} from "./model-catalog-data.js";

export { BUILT_IN_MODELS, MODELS_DEV_SOURCE };

const MAX_SAFE_TOKENS = BigInt(Number.MAX_SAFE_INTEGER);
const SNAPSHOT_RULES = Object.freeze([
  Object.freeze({
    pattern: /^claude-haiku-4-5-(\d{8})$/i,
    entryId: "claude-haiku-4-5-20251001",
  }),
]);

export function parseTokenLimit(value, { optional = false } = {}) {
  if (
    value === null ||
    value === undefined ||
    (typeof value === "string" && value.trim() === "")
  ) {
    if (optional) {
      return null;
    }
    throw new RangeError("token limit is required");
  }

  if (typeof value === "number") {
    if (Number.isSafeInteger(value) && value > 0) {
      return value;
    }
    throw new RangeError("token limit must be a positive safe integer");
  }
  if (typeof value !== "string") {
    throw new TypeError("token limit must be a string or number");
  }

  const normalized = value.trim();
  if (/^\d+$/.test(normalized)) {
    return safeTokenNumber(BigInt(normalized));
  }

  const suffixed = normalized.match(
    /^(?:(\d+)(?:\.(\d+))?|\.(\d+))([kKmM])$/,
  );
  if (!suffixed) {
    throw new RangeError("invalid token limit");
  }

  const whole = suffixed[1] || "0";
  const fraction = suffixed[2] ?? suffixed[3] ?? "";
  const numerator = BigInt(`${whole}${fraction}`);
  const denominator = 10n ** BigInt(fraction.length);
  const multiplier = suffixed[4].toLowerCase() === "k"
    ? 1_000n
    : 1_000_000n;
  const scaled = numerator * multiplier;
  if (scaled % denominator !== 0n) {
    throw new RangeError("token limit must resolve to a whole token");
  }
  return safeTokenNumber(scaled / denominator);
}

export function createModelCatalogMatcher(models) {
  const entries = [...models].sort(compareEntries);
  const exact = buildIndex(entries, (entry) => [entry.id]);
  const apiIds = buildIndex(entries, (entry) => entry.apiIds ?? []);
  const aliases = buildIndex(entries, (entry) => [
    entry.canonicalId,
    ...(entry.aliases ?? []),
  ]);
  const compatibility = buildIndex(
    entries,
    (entry) => entry.compatibilityAliases ?? [],
  );
  const wrapperSafe = buildIndex(entries, (entry) => [
    entry.id,
    ...(entry.apiIds ?? []),
    entry.canonicalId,
    ...(entry.aliases ?? []),
  ]);
  const candidateKeys = new Map(entries.map((entry) => [
    entry,
    normalizedKeys([
      entry.id,
      entry.canonicalId,
      ...(entry.apiIds ?? []),
      ...(entry.aliases ?? []),
      ...(entry.compatibilityAliases ?? []),
    ]),
  ]));

  function matchModelSuggestionsForCatalog(id, { limit = 5 } = {}) {
    const normalized = normalizeModelId(id);
    const cappedLimit = normalizeSuggestionLimit(limit);
    if (!normalized || cappedLimit === 0) {
      return [];
    }

    const exactOwners = exact.get(normalized);
    if (exactOwners?.length) {
      return suggestions(
        exactOwners,
        "exact",
        exactOwners.length === 1,
        cappedLimit,
      );
    }

    const aliasOwners = indexOwners(normalized, apiIds, aliases);
    if (aliasOwners.length) {
      return suggestions(
        aliasOwners,
        "alias",
        aliasOwners.length === 1,
        cappedLimit,
      );
    }

    const compatibilityOwners = compatibility.get(normalized);
    if (compatibilityOwners?.length) {
      return suggestions(
        compatibilityOwners,
        "compatibility",
        compatibilityOwners.length === 1,
        cappedLimit,
      );
    }

    for (const snapshotRule of SNAPSHOT_RULES) {
      const snapshotMatch = normalized.match(snapshotRule.pattern);
      if (!snapshotMatch) {
        continue;
      }
      if (!isValidYYYYMMDD(snapshotMatch[1])) {
        return [];
      }
      const owners = exact.get(normalizeModelId(snapshotRule.entryId));
      if (owners?.length) {
        return suggestions(
          owners,
          "snapshot",
          false,
          cappedLimit,
        );
      }
      return [];
    }

    const wrappers = matchWrapperEntries(normalized, wrapperSafe);
    if (wrappers.length) {
      return suggestions(
        wrappers,
        "wrapper",
        wrappers.length === 1,
        cappedLimit,
      );
    }

    const family = entries.filter((entry) =>
      candidateKeys.get(entry).some((key) =>
        isAnchoredFamilyMatch(normalized, key)
      )
    );
    return suggestions(family, "family", false, cappedLimit);
  }

  return Object.freeze({
    matchModelSuggestions: matchModelSuggestionsForCatalog,
    matchModelSuggestion(id) {
      return matchModelSuggestionsForCatalog(id)[0] ?? null;
    },
  });
}

let activeCatalog = deepFreeze({
  source: { ...MODELS_DEV_SOURCE },
  models: BUILT_IN_MODELS.map((entry) => ({ ...entry })),
});
let activeMatcher = createModelCatalogMatcher(activeCatalog.models);

export function installModelCatalog(catalog) {
  if (!catalog || typeof catalog !== "object" || !Array.isArray(catalog.models)) {
    throw new TypeError("model catalog must contain models");
  }
  if (!catalog.source || typeof catalog.source !== "object") {
    throw new TypeError("model catalog source is required");
  }
  const source = { ...catalog.source };
  const models = catalog.models.map((entry) => ({
    ...entry,
    apiIds: [...(entry.apiIds || [])],
    aliases: [...(entry.aliases || [])],
    compatibilityAliases: [...(entry.compatibilityAliases || [])],
    references: (entry.references || []).map((reference) => ({ ...reference })),
    source: { ...(entry.source || source) },
  }));
  const matcher = createModelCatalogMatcher(models);
  activeCatalog = deepFreeze({ source, models });
  activeMatcher = matcher;
  return activeCatalog;
}

export function currentModelCatalog() {
  return activeCatalog;
}

export function matchModelSuggestions(id, { limit = 5 } = {}) {
  return activeMatcher.matchModelSuggestions(id, { limit });
}

export function matchModelSuggestion(id) {
  return activeMatcher.matchModelSuggestion(id);
}

export function applyModelSuggestion(model, match) {
  const applied = { ...model };
  if (!match?.entry) {
    return applied;
  }

  for (const field of [
    "context_window",
    "max_output_tokens",
    "supports_vision",
    "supports_tools",
    "supports_structured_output",
    "input_price_micro_usd_per_million",
    "output_price_micro_usd_per_million",
    "cache_read_price_micro_usd_per_million",
    "cache_write_price_micro_usd_per_million",
  ]) {
    if (isEmpty(applied[field]) && Object.hasOwn(match.entry, field)) {
      applied[field] = match.entry[field];
    }
  }
  return applied;
}

function safeTokenNumber(value) {
  if (value <= 0n || value > MAX_SAFE_TOKENS) {
    throw new RangeError("token limit must be a positive safe integer");
  }
  return Number(value);
}

function suggestion(entry, match, autoApply) {
  return { entry, match, autoApply };
}

function buildIndex(entries, keysForEntry) {
  const index = new Map();
  for (const entry of entries) {
    for (const key of normalizedKeys(keysForEntry(entry))) {
      const owners = index.get(key) ?? [];
      if (!owners.includes(entry)) {
        owners.push(entry);
        index.set(key, owners);
      }
    }
  }
  for (const [key, owners] of index) {
    index.set(key, Object.freeze(owners));
  }
  return index;
}

function normalizedKeys(keys) {
  return [...new Set(keys.map(normalizeModelId).filter(Boolean))];
}

function indexOwners(key, ...indexes) {
  const owners = new Set();
  for (const index of indexes) {
    for (const entry of index.get(key) ?? []) {
      owners.add(entry);
    }
  }
  return [...owners].sort(compareEntries);
}

function normalizeModelId(id) {
  if (typeof id !== "string") {
    return null;
  }
  const normalized = id.trim().toLowerCase();
  return normalized || null;
}

function normalizeSuggestionLimit(limit) {
  if (typeof limit !== "number" || !Number.isFinite(limit)) {
    return 5;
  }
  return Math.min(5, Math.max(0, Math.trunc(limit)));
}

function matchWrapperEntries(id, exactIndex) {
  const matched = new Set();
  for (const [key, owners] of exactIndex) {
    const boundary = id.length - key.length - 1;
    const separator = id[boundary];
    if (
      boundary <= 0 ||
      id.slice(boundary + 1) !== key ||
      !"/:-".includes(separator)
    ) {
      continue;
    }
    const wrapper = id.slice(0, boundary);
    if (!isValidWrapperNamespace(wrapper, separator)) {
      continue;
    }
    for (const owner of owners) {
      matched.add(owner);
    }
  }
  return [...matched].sort(compareEntries);
}

function isAnchoredFamilyMatch(id, key) {
  if (/^(?:latest|preview|\d{8})$/.test(id)) {
    return false;
  }
  if (id.startsWith(`${key}-`)) {
    return isFamilyQualifier(id.slice(key.length + 1));
  }
  if (key.startsWith(`${id}-`)) {
    return isFamilyQualifier(key.slice(id.length + 1));
  }
  return false;
}

function isFamilyQualifier(value) {
  return /^[a-z]/.test(value) ||
    (/^\d{8}$/.test(value) && isValidYYYYMMDD(value));
}

function isValidWrapperNamespace(namespace, separator) {
  const pattern = separator === "-"
    ? /^[a-z0-9][a-z0-9._]*$/
    : /^[a-z0-9][a-z0-9._-]*$/;
  return pattern.test(namespace);
}

function isValidYYYYMMDD(value) {
  if (!/^\d{8}$/.test(value)) {
    return false;
  }
  const year = Number(value.slice(0, 4));
  const month = Number(value.slice(4, 6));
  const day = Number(value.slice(6, 8));
  if (year < 1 || month < 1 || month > 12 || day < 1) {
    return false;
  }
  const leap = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0);
  const daysInMonth = [
    31,
    leap ? 29 : 28,
    31,
    30,
    31,
    30,
    31,
    31,
    30,
    31,
    30,
    31,
  ];
  return day <= daysInMonth[month - 1];
}

function suggestions(entries, match, autoApply, limit) {
  return entries
    .slice(0, limit)
    .map((entry) => suggestion(entry, match, autoApply));
}

function compareEntries(left, right) {
  const leftKey = entrySortKey(left);
  const rightKey = entrySortKey(right);
  if (leftKey < rightKey) {
    return -1;
  }
  if (leftKey > rightKey) {
    return 1;
  }
  return 0;
}

function entrySortKey(entry) {
  return [
    entry.canonicalId ?? "",
    entry.id ?? "",
    entry.provider ?? "",
    entry.name ?? "",
  ].join("\0").toLowerCase();
}

function isEmpty(value) {
  return value === "" || value === null || value === undefined;
}

function deepFreeze(value) {
  if (value && typeof value === "object" && !Object.isFrozen(value)) {
    for (const nested of Object.values(value)) {
      deepFreeze(nested);
    }
    Object.freeze(value);
  }
  return value;
}
