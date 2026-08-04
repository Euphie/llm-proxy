export function statsURLParameters() {
	try {
		return new URLSearchParams(globalThis.location?.search || "");
	} catch {
		return new URLSearchParams();
	}
}

export function replaceStatsURL(path, filters = {}) {
	if (typeof globalThis.history?.replaceState !== "function") return;
	const query = new URLSearchParams();
	for (const [name, raw] of Object.entries(filters)) {
		const value = String(raw ?? "").trim();
		if (value !== "") query.set(name, value);
	}
	const suffix = query.toString();
	globalThis.history.replaceState(null, "", `${path}${suffix ? `?${suffix}` : ""}`);
}

export function positivePageParameter(parameters, name, fallback, allowed = null) {
	const value = Number(parameters.get(name));
	if (!Number.isSafeInteger(value) || value < 1 || value > 1_000_000) return fallback;
	if (allowed && !allowed.includes(value)) return fallback;
	return value;
}

export function localDateTimeValue(value) {
	if (!value) return "";
	const date = new Date(value);
	if (Number.isNaN(date.getTime())) return "";
	const local = new Date(date.getTime() - date.getTimezoneOffset() * 60_000);
	return local.toISOString().slice(0, 16);
}

export function dateValue(value) {
	if (!value) return "";
	const date = new Date(value);
	return Number.isNaN(date.getTime()) ? "" : date.toISOString().slice(0, 10);
}
