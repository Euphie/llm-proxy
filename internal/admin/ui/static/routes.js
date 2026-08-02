export const PROFILE_SECTIONS = new Set([
  "overview",
  "connection",
  "models",
  "routing",
  "vision",
  "reliability",
  "agents",
]);

export function parseAdminRoute(pathname) {
  if (pathname === "/_admin" || pathname === "/_admin/") {
    return { page: "overview" };
  }
  if (pathname === "/_admin/setup") {
    return { page: "setup" };
  }
  if (pathname === "/_admin/profiles") {
    return { page: "profiles" };
  }
  if (pathname === "/_admin/stats") {
    return { page: "stats" };
  }
  if (pathname === "/_admin/system") {
    return { page: "system" };
  }

  const match = String(pathname ?? "").match(
    /^\/_admin\/profiles\/([1-9]\d*)\/([^/]+)$/,
  );
  if (!match || !PROFILE_SECTIONS.has(match[2])) {
    return { page: "not-found" };
  }
  const profileId = Number(match[1]);
  if (!Number.isSafeInteger(profileId)) {
    return { page: "not-found" };
  }
  return { page: "profile", profileId, section: match[2] };
}

export function profileSectionHref(profileId, section) {
  const id = Number(profileId);
  if (!Number.isSafeInteger(id) || id <= 0) {
    throw new Error("Profile ID 必须是正整数。");
  }
  if (!PROFILE_SECTIONS.has(section)) {
    throw new Error("Profile 页面无效。");
  }
  return `/_admin/profiles/${id}/${section}`;
}
