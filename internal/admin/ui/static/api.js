export class APIError extends Error {
  constructor(status, code, message, fields = {}) {
    super(message);
    this.name = "APIError";
    this.status = status;
    this.code = code;
    this.fields = fields;
  }
}

export function readCookie(name) {
  const prefix = `${encodeURIComponent(name)}=`;
  for (const item of document.cookie.split(";")) {
    const cookie = item.trim();
    if (cookie.startsWith(prefix)) {
      return decodeURIComponent(cookie.slice(prefix.length));
    }
  }
  return "";
}

export async function request(path, options = {}) {
  const {
    body: suppliedBody,
    headers: suppliedHeaders,
    ...fetchOptions
  } = options;
  const method = (fetchOptions.method || "GET").toUpperCase();
  const headers = new Headers(suppliedHeaders);

  if (method !== "GET") {
    headers.set("X-CSRF-Token", readCookie("llm_proxy_csrf"));
  }

  let body = suppliedBody;
  if (body === undefined && method !== "GET" && method !== "HEAD") {
    body = {};
  }
  if (body !== undefined && typeof body === "object") {
    headers.set("Content-Type", "application/json");
    body = JSON.stringify(body);
  }

  const response = await fetch(path, {
    ...fetchOptions,
    method,
    headers,
    credentials: "same-origin",
    ...(body === undefined ? {} : { body }),
  });

  const text = await response.text();
  let payload = null;
  if (text !== "") {
    try {
      payload = JSON.parse(text);
    } catch {
      payload = text;
    }
  }

  if (!response.ok) {
    const details =
      payload && typeof payload === "object" && payload.error
        ? payload.error
        : {};
    throw new APIError(
      response.status,
      details.code || "request_failed",
      details.message || `Request failed with status ${response.status}.`,
      details.fields || {},
    );
  }

  return payload;
}

export const api = {
  session: () => request("/_admin/api/session"),
  login: (username, password) =>
    request("/_admin/api/login", {
      method: "POST",
      body: { username, password },
    }),
  logout: () =>
    request("/_admin/api/logout", {
      method: "POST",
    }),
  changePassword: (currentPassword, newPassword) =>
    request("/_admin/api/password", {
      method: "POST",
      body: {
        current_password: currentPassword,
        new_password: newPassword,
      },
    }),
};

Object.assign(api, {
  listProfiles: () => request("/_admin/api/profiles"),
  getProfile: (id) => request(`/_admin/api/profiles/${id}`),
  createProfile: (body) =>
    request("/_admin/api/profiles", {
      method: "POST",
      body,
    }),
  updateProfile: (id, body) =>
    request(`/_admin/api/profiles/${id}`, {
      method: "PUT",
      body,
    }),
  copyProfile: (id, body) =>
    request(`/_admin/api/profiles/${id}/copy`, {
      method: "POST",
      body,
    }),
  deleteProfile: (id, replacementDefaultId = 0) =>
    request(
      `/_admin/api/profiles/${id}${
        replacementDefaultId > 0
          ? `?replacement_default_id=${replacementDefaultId}`
          : ""
      }`,
      {
        method: "DELETE",
      },
    ),
  setDefaultProfile: (profileId) =>
    request("/_admin/api/default-profile", {
      method: "PUT",
      body: { profile_id: profileId },
    }),
  listStrategies: (profileId) =>
    request(`/_admin/api/profiles/${profileId}/strategies`),
  createStrategy: (profileId, config) =>
    request(`/_admin/api/profiles/${profileId}/strategies`, {
      method: "POST",
      body: { config },
    }),
  updateStrategy: (profileId, strategyId, config) =>
    request(`/_admin/api/profiles/${profileId}/strategies/${strategyId}`, {
      method: "PUT",
      body: { config },
    }),
  advanceStrategy: (profileId, strategyId, from, to) =>
    request(
      `/_admin/api/profiles/${profileId}/strategies/${strategyId}/advance`,
      { method: "POST", body: { from, to } },
    ),
  startStrategyCanary: (profileId, strategyId, canaryBps, expectedRevision) =>
    request(
      `/_admin/api/profiles/${profileId}/strategies/${strategyId}/canary`,
      {
        method: "POST",
        body: {
          canary_bps: canaryBps,
          expected_revision: expectedRevision,
        },
      },
    ),
  cancelStrategyCanary: (profileId, expectedRevision) =>
    request(`/_admin/api/profiles/${profileId}/strategies/cancel-canary`, {
      method: "POST",
      body: { expected_revision: expectedRevision },
    }),
  promoteStrategy: (profileId, expectedRevision) =>
    request(`/_admin/api/profiles/${profileId}/strategies/promote`, {
      method: "POST",
      body: { expected_revision: expectedRevision },
    }),
  rollbackStrategy: (profileId, expectedRevision) =>
    request(`/_admin/api/profiles/${profileId}/strategies/rollback`, {
      method: "POST",
      body: { expected_revision: expectedRevision },
    }),
	generateStrategyCandidate: (profileId) =>
		request(`/_admin/api/profiles/${profileId}/strategies/generate-candidate`, {
			method: "POST",
		}),
  stats: (params = {}) => {
    const query = new URLSearchParams(
      Object.entries(params)
        .map(([key, value]) => [key, String(value ?? "").trim()])
        .filter(([, value]) => value !== ""),
    ).toString();
    return request(`/_admin/api/stats${query === "" ? "" : `?${query}`}`);
  },
  routingTraces: (params = {}) => {
    const query = new URLSearchParams(
      Object.entries(params)
        .map(([key, value]) => [key, String(value ?? "").trim()])
        .filter(([, value]) => value !== ""),
    ).toString();
    return request(
      `/_admin/api/routing-traces${query === "" ? "" : `?${query}`}`,
    );
  },
	routingTrace: (id) => request(`/_admin/api/routing-traces/${Number(id)}`),
	modelPerformance: (params = {}) => {
		const query = new URLSearchParams(
			Object.entries(params)
				.map(([key, value]) => [key, String(value ?? "").trim()])
				.filter(([, value]) => value !== ""),
		).toString();
		return request(`/_admin/api/model-performance${query === "" ? "" : `?${query}`}`);
	},
  system: () => request("/_admin/api/system"),
  modelCatalog: () => request("/_admin/api/model-catalog"),
  refreshModelCatalog: () =>
    request("/_admin/api/model-catalog/refresh", {
      method: "POST",
    }),
});
