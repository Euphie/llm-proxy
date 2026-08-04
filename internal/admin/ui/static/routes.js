export const PROFILE_SECTIONS = new Set([
  "overview",
  "connection",
  "models",
  "routing",
  "vision",
  "reliability",
  "agents",
]);

export const HELP_TOPICS = new Set([
  "overview",
  "glossary",
  "profiles-models",
  "agents",
  "vision",
  "intelligent-routing",
  "reliability",
  "statistics",
]);

export const HELP_OVERVIEW_HREF = "/_admin/help/overview";

export const INTELLIGENT_ROUTING_HELP_HREF =
  "/_admin/help/intelligent-routing";

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
	return { page: "stats", section: "usage" };
	}
	if (pathname === "/_admin/stats/routing") {
		return { page: "stats", section: "routing" };
	}
	if (pathname === "/_admin/stats/models") {
		return { page: "stats", section: "models" };
  }
  if (pathname === "/_admin/system") {
    return { page: "system" };
  }
  const helpMatch = String(pathname ?? "").match(/^\/_admin\/help\/([^/]+)$/);
  if (helpMatch && HELP_TOPICS.has(helpMatch[1])) {
    return { page: "help", topic: helpMatch[1] };
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

export function helpHref(topic) {
  if (!HELP_TOPICS.has(topic)) {
    throw new Error("帮助主题无效。");
  }
  return `/_admin/help/${topic}`;
}
