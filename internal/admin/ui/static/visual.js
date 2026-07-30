export const PROFILE_ICON_PALETTES = Object.freeze([
  "profile-icon-blue",
  "profile-icon-cyan",
  "profile-icon-green",
  "profile-icon-orange",
  "profile-icon-red",
  "profile-icon-yellow",
]);

export function stablePaletteIndex(
  slug,
  paletteSize = PROFILE_ICON_PALETTES.length,
) {
  const normalized = String(slug ?? "").trim().toLowerCase();
  if (normalized === "" || !Number.isInteger(paletteSize) || paletteSize <= 0) {
    return 0;
  }

  let hash = 2166136261;
  for (const character of normalized) {
    hash ^= character.codePointAt(0);
    hash = Math.imul(hash, 16777619);
  }
  return (hash >>> 0) % paletteSize;
}

export function profileIconVisual(profile) {
  const slug = String(profile?.slug ?? "").trim();
  const source = String(profile?.display_name ?? "").trim() || slug || "?";
  const label = Array.from(source)[0]?.toLocaleUpperCase() || "?";

  return {
    label,
    className:
      PROFILE_ICON_PALETTES[stablePaletteIndex(slug)] ??
      PROFILE_ICON_PALETTES[0],
  };
}
