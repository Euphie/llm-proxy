export {
  defaultProfileDraft,
  profileDraft,
  profilePayload,
} from "./profiles.js";

export function mergeProfileSection(profile, section, values) {
  const merged = structuredClone(profile);
  const config = merged.config || (merged.config = {});

  if (section === "connection") {
    for (const field of ["slug", "display_name", "enabled", "make_default"]) {
      if (Object.hasOwn(values, field)) {
        merged[field] = values[field];
      }
    }
    for (const field of [
      "version",
      "protocol",
      "upstream",
    ]) {
      if (Object.hasOwn(values, field)) {
        config[field] = values[field];
      }
    }
    return merged;
  }

  const configFields = {
    models: "models",
    routing: "auto_routing",
    vision: "vision",
    reliability: "overload_rules",
  };
  const field = configFields[section];
  if (!field) {
    throw new Error("Profile 配置页面无效。");
  }
  config[field] = structuredClone(
    Object.hasOwn(values, field) ? values[field] : values,
  );
  return merged;
}
