import { api } from "./api.js";
import {
  nextScreen,
  renderLogin,
  renderPasswordChange,
} from "./auth.js";
import { createWindowFrame } from "./chrome.js";
import { renderDashboard } from "./dashboard.js";
import { renderOnboarding } from "./onboarding.js";
import { renderProfileCreationWizard } from "./profile-create-wizard.js";
import { installModelCatalog } from "./model-catalog.js";
import {
  defaultProfileDraft,
  profileDraft,
  profilePayload,
  renderProfileEditor as renderLegacyProfileEditor,
  renderProfileList,
} from "./profiles.js";
import { runEvaluationCatalogUpdate } from "./evaluation-catalog.js";
import { renderProfileEditor as renderProfileSectionEditor } from "./profile-editor.js";
import { renderHelpPage } from "./routing-guide.js";
import { renderStatsPage } from "./stats.js";
import { renderSystemPage } from "./system.js";
import { parseAdminRoute, profileSectionHref } from "./routes.js";

function policyToAutoRouting(policy, enabled) {
  const roles = policy?.roles || {};
  return {
    enabled,
    participants: [...(roles.participants || [])],
    strong_baseline_model: roles.strong_baseline_model || "",
    task_analyzer_model: roles.task_analyzer_model || "",
    analyzer_timeout: policy?.analyzer_timeout || "",
    analyzer_min_confidence_bps: policy?.analyzer_min_confidence_bps || 0,
    session_ttl: policy?.session_ttl || "",
    session_lock_token_threshold: Number(policy?.session_lock_token_threshold || 100000),
    self_escalation: structuredClone(policy?.self_escalation || {}),
    dynamic_optimization: {
      ...structuredClone(policy?.dynamic_optimization || {}),
      reviewer_model: roles.reviewer_model || policy?.dynamic_optimization?.reviewer_model || "",
    },
    strategy: structuredClone(policy),
  };
}

function autoRoutingToPolicy(auto) {
  const policy = structuredClone(auto?.strategy || {});
  policy.roles = {
    participants: [...(auto?.participants || [])],
    strong_baseline_model: auto?.strong_baseline_model || "",
    task_analyzer_model: auto?.task_analyzer_model || "",
    reviewer_model: auto?.dynamic_optimization?.reviewer_model || "",
  };
  policy.analyzer_timeout = auto?.analyzer_timeout || "";
  policy.analyzer_min_confidence_bps = Number(auto?.analyzer_min_confidence_bps || 0);
  policy.session_ttl = auto?.session_ttl || "";
  policy.session_lock_token_threshold = Number(auto?.session_lock_token_threshold || 100000);
  policy.self_escalation = structuredClone(auto?.self_escalation || {});
  policy.dynamic_optimization = structuredClone(auto?.dynamic_optimization || {});
  return policy;
}

export async function bootstrap({
  root = document.querySelector("#app"),
  client = api,
  generateProfile = () => {},
  path = currentPath(),
} = {}) {
  if (!root) {
    return;
  }
  try {
    const session = await client.session();
    await renderSession(root, client, session, generateProfile, path);
  } catch (error) {
    if (isUnauthorized(error)) {
      renderLoginScreen(root, client, generateProfile, path);
      return;
    }
    renderFatalError(root, error);
  }
}

async function renderSession(root, client, session, generateProfile, path) {
  const screen = nextScreen({
    ...session,
    authenticated: true,
  });
  if (screen === "password-change") {
    renderPasswordScreen(root, client, generateProfile, path);
    return;
  }
  const needsSetup = session?.initialization_state === "profile_setup_required" &&
    (path === "/_admin" || path === "/_admin/");
  const effectivePath = needsSetup ? "/_admin/setup" : path;
  if (needsSetup && typeof window !== "undefined") {
    window.history?.replaceState?.(null, "", effectivePath);
  }
  await renderAuthenticated(
    root,
    client,
    session,
    generateProfile,
    effectivePath,
  );
}

function renderLoginScreen(root, client, generateProfile, path) {
  root.className = "";
  renderLogin(root, {
    login: async (username, password) => {
      const session = await client.login(username, password);
      await renderSession(root, client, session, generateProfile, path);
    },
  });
}

function renderPasswordScreen(root, client, generateProfile, path) {
  root.className = "";
  renderPasswordChange(root, {
    changePassword: async (currentPassword, newPassword) => {
      try {
        await client.changePassword(currentPassword, newPassword);
        const session = await client.session();
        await renderSession(root, client, session, generateProfile, path);
      } catch (error) {
        if (isUnauthorized(error)) {
          renderLoginScreen(root, client, generateProfile, path);
          return;
        }
        throw error;
      }
    },
  });
}

async function renderAuthenticated(root, client, session, generateProfile, path) {
  const route = parseAdminRoute(path);
  const pageName = navigationPage(route);
  const stage = document.createElement("div");
  stage.className = "desktop-stage";
  const sidebar = document.createElement("aside");
  sidebar.className = "app-sidebar";
  const brand = document.createElement("p");
  brand.className = "app-brand";
  brand.textContent = "llm-proxy";
  const navigation = document.createElement("nav");
  navigation.setAttribute("aria-label", "主导航");
  const list = document.createElement("ul");
  list.className = "app-nav";

  for (const item of [
    {
      name: "overview",
      text: "概览",
      href: "/_admin/",
      iconClass: "nav-icon-mint",
      iconText: "⌂",
    },
    {
      name: "profiles",
      text: "Profiles",
      href: "/_admin/profiles",
      iconClass: "nav-icon-blue",
      iconText: "P",
    },
    {
      name: "stats",
      text: "统计",
      href: "/_admin/stats",
      iconClass: "nav-icon-orange",
      iconText: "S",
    },
    {
      name: "system",
      text: "系统",
      href: "/_admin/system",
      iconClass: "nav-icon-gray",
      iconText: "⚙",
    },
    {
      name: "help",
      text: "帮助",
      href: "/_admin/help/overview",
      iconClass: "nav-icon-violet",
      iconText: "?",
    },
  ]) {
    const entry = document.createElement("li");
    const link = document.createElement("a");
    link.textContent = item.text;
    link.setAttribute("href", item.href);
    const icon = document.createElement("span");
    icon.className = `app-nav-icon ${item.iconClass}`;
    icon.textContent = item.iconText;
    icon.setAttribute("aria-hidden", "true");
    link.append(icon);
    if (item.name === pageName) {
      link.setAttribute("aria-current", "page");
    }
    entry.append(link);
    list.append(entry);
  }

  const logoutEntry = document.createElement("li");
  const logout = document.createElement("button");
  logout.type = "button";
  logout.textContent = "退出";
  logoutEntry.append(logout);
  list.append(logoutEntry);
  navigation.append(list);
  sidebar.append(brand, navigation);

  const content = document.createElement("section");
  content.className = "app-content";
  const headingGroup = document.createElement("div");
  headingGroup.className = "page-heading";
  const heading = document.createElement("h1");
  heading.textContent = {
    overview: "概览",
    profiles: "Profiles",
    stats: "统计",
    system: "系统",
    help: "帮助",
    "not-found": "页面不存在",
  }[pageName];
  const alert = document.createElement("div");
  alert.className = "app-flash error-banner";
  alert.setAttribute("role", "alert");
  alert.hidden = true;
  const workspace = document.createElement("div");
  workspace.className = "profile-workspace";
  headingGroup.append(heading);
  content.append(headingGroup, workspace);
  let flashTimeoutID = null;

  function clearFlashTimeout() {
    if (flashTimeoutID !== null) {
      clearTimeout(flashTimeoutID);
      flashTimeoutID = null;
    }
  }

  function showAppError(error) {
    clearFlashTimeout();
    alert.className = "app-flash error-banner";
    alert.setAttribute("role", "alert");
    alert.textContent = error?.message || "请求失败，请重试。";
    alert.hidden = false;
  }

  function showAppSuccess(message) {
    clearFlashTimeout();
    alert.className = "app-flash success-banner";
    alert.setAttribute("role", "status");
    alert.textContent = message;
    alert.hidden = false;
    flashTimeoutID = setTimeout(() => {
      alert.hidden = true;
      alert.textContent = "";
      flashTimeoutID = null;
    }, 4000);
  }

  async function performEvaluationCatalogUpdate(onProgress) {
    try {
      const job = await runEvaluationCatalogUpdate({
        start: () => client.startEvaluationCatalogUpdate(),
        status: () => client.evaluationCatalogUpdateStatus(),
        onProgress,
      });
      const catalog = await client.evaluationCatalog();
      return { ...job, catalog };
    } catch (error) {
      return {
        ...(error?.job || {}),
        state: error?.job?.state || "failed",
        error: error?.message || "公开评测更新失败，仍在使用之前的数据。",
      };
    }
  }

  function notifyEvaluationCatalogUpdate(job) {
    if (job?.state === "succeeded") {
      showAppSuccess(
        job.changed ? "公开评测更新完成。" : "公开评测已是最新版本。",
      );
      return;
    }
    showAppError(new Error("公开评测更新失败，仍在使用之前的数据。"));
  }

  async function runMutation(mutation, after = showList) {
    try {
      await mutation();
      await after();
    } catch (error) {
      if (isUnauthorized(error)) {
        renderLoginScreen(root, client, generateProfile, path);
        return;
      }
      throw error;
    }
  }

  async function showEditor(draft) {
    alert.hidden = true;
    alert.textContent = "";
		let routingPolicyOverview = null;
		let modelDirectoryOverview = null;
		if (draft.id && Number(draft.config?.version) === 2) {
			[routingPolicyOverview, modelDirectoryOverview] = await Promise.all([
				client.routingPolicy(draft.id),
				client.profileModels(draft.id),
			]);
			draft.config.models = (modelDirectoryOverview.models || []).map((model) =>
				structuredClone(model.capability));
			const policy = routingPolicyOverview.active?.policy || null;
			if (policy) {
				draft.config.auto_routing = policyToAutoRouting(
					policy,
					Boolean(draft.config.auto_routing?.enabled),
				);
			}
    }
		draft.routing_policy_overview = routingPolicyOverview;
		draft.model_directory_overview = modelDirectoryOverview;

    const editorActions = {
      save: async (payload) => {
        await runMutation(async () => {
          if (draft.id) {
			if (Number(draft.config?.version) === 2) {
				payload.expected_runtime_revision = Number(
					draft.routing_policy_overview?.runtime_state?.revision || 0,
				);
			}
            await client.updateProfile(draft.id, payload);
            return;
          }
          await client.createProfile(payload);
        }, route.page === "profile" ? showProfileRoute : showList);
        showAppSuccess("Profile 保存成功。");
      },
      cancel: () => {
        void showList();
      },
      generate: generateProfile,
		applyRoutingPolicy: async (revision, policy, reason) => {
			await client.applyRoutingPolicy(draft.id, revision, policy, reason);
			showAppSuccess("策略已保存并立即生效。");
			await showProfileRoute();
		},
		generateRoutingPolicy: (intent) => client.generateRoutingPolicy(draft.id, intent),
		rollbackRoutingPolicy: async (revision, versionID, reason) => {
			await client.rollbackRoutingPolicy(draft.id, revision, versionID, reason);
			showAppSuccess("历史策略已复制为新版本并立即生效。");
			await showProfileRoute();
		},
		addProfileModel: async (revision, modelID, capability, reason) => {
			await client.addProfileModel(draft.id, revision, modelID, capability, reason);
			showAppSuccess("模型已加入目录并立即生效。");
			await showProfileRoute();
		},
		addProfileModels: async (revision, capabilities, reason) => {
			let currentRevision = Number(revision || 0);
			for (const capability of capabilities) {
				const directory = await client.addProfileModel(
					draft.id,
					currentRevision,
					capability.id,
					capability,
					reason,
				);
				currentRevision = Number(directory.runtime_state?.revision || currentRevision);
			}
			showAppSuccess(`已导入 ${capabilities.length} 个模型并立即生效。`);
			await showProfileRoute();
		},
		updateProfileModel: async (revision, modelID, capability, reason) => {
			await client.updateProfileModel(draft.id, revision, modelID, capability, reason);
			showAppSuccess("模型能力已更新并立即生效。");
			await showProfileRoute();
		},
		offlineProfileModel: async (body) => {
			await client.offlineProfileModel(draft.id, body);
			showAppSuccess("模型已紧急下线，新策略已立即生效。");
			await showProfileRoute();
		},
		restoreProfileModel: async (revision, modelID, reason) => {
			await client.restoreProfileModel(draft.id, revision, modelID, reason);
			showAppSuccess("模型已恢复并立即生效。");
			await showProfileRoute();
		},
		retireProfileModel: async (revision, modelID, reason) => {
			await client.retireProfileModel(draft.id, revision, modelID, reason);
			showAppSuccess("模型已退役；该 ID 将保留为不可复用记录。");
			await showProfileRoute();
		},
    };
    if (route.page === "profile") {
      renderProfileSectionEditor(workspace, draft, {
        ...editorActions,
        section: route.section,
        defaultProfileID: draft.make_default ? draft.id : 0,
      });
      return;
    }
    renderLegacyProfileEditor(workspace, draft, editorActions);
  }

  async function openCreationWizard(draft) {
    try {
      await renderProfileCreationWizard(workspace, draft, {
        createProfile: (payload) => client.createProfile(payload),
        updateProfile: async (profileID, payload) => {
          let directory = await client.profileModels(profileID);
          let revision = Number(directory.runtime_state?.revision || 0);
          const current = new Map((directory.models || []).map((model) => [model.model_id, model]));
          for (const capability of payload.config?.models || []) {
            const model = current.get(capability.id);
            if (!model) {
              directory = await client.addProfileModel(
                profileID, revision, capability.id, capability, "Profile 创建向导添加模型",
              );
              revision = Number(directory.runtime_state?.revision || 0);
              continue;
            }
            if (JSON.stringify(model.capability) !== JSON.stringify(capability)) {
              directory = await client.updateProfileModel(
                profileID, revision, capability.id, capability, "Profile 创建向导更新模型",
              );
              revision = Number(directory.runtime_state?.revision || 0);
            }
          }
          const auto = payload.config?.auto_routing || {};
          if (auto.enabled) {
            const overview = await client.routingPolicy(profileID);
            const applied = await client.applyRoutingPolicy(
              profileID,
              Number(overview.runtime_state?.revision || revision),
              autoRoutingToPolicy(auto),
              "Profile 创建向导启用智能路由",
            );
            revision = Number(applied.runtime_state?.revision || revision);
          }
          payload.expected_runtime_revision = revision;
          const updated = await client.updateProfile(profileID, payload);
          return {
            ...updated,
            config: { ...structuredClone(payload.config), version: 2 },
          };
        },
        loadEvaluationCatalog: typeof client.evaluationCatalog === "function"
          ? () => client.evaluationCatalog()
          : undefined,
		updateEvaluationCatalog:
			typeof client.startEvaluationCatalogUpdate === "function" &&
			typeof client.evaluationCatalogUpdateStatus === "function"
			? async (onProgress) => {
				const job = await performEvaluationCatalogUpdate(onProgress);
				notifyEvaluationCatalogUpdate(job);
				return job;
			}
			: undefined,
        onSaved: (message) => showAppSuccess(message),
        cancel: () => { void showList(); },
        complete: (profile) => {
          showAppSuccess("Profile 创建完成。");
          navigateTo(profileSectionHref(profile.id, "overview"));
        },
      });
    } catch (error) {
      if (isUnauthorized(error)) {
        renderLoginScreen(root, client, generateProfile, path);
        return;
      }
      showAppError(error);
    }
  }

  async function showList() {
    alert.hidden = true;
    alert.textContent = "";
    try {
      const data =
        typeof client.listProfiles === "function"
          ? await client.listProfiles()
          : { default_profile_id: 0, profiles: [] };
      renderProfileList(workspace, data, {
        create: () => {
          const draft = defaultProfileDraft();
          draft.make_default = data.profiles.length === 0;
          void openCreationWizard(draft);
        },
        edit: (profile) =>
          navigateTo(profileSectionHref(profile.id, "overview")),
        generate: generateProfile,
        copy: async (profile, body) => {
          await runMutation(() => client.copyProfile(profile.id, body));
        },
        setDefault: async (profile) => {
          await runMutation(() => client.setDefaultProfile(profile.id));
        },
        toggle: async (profile) => {
          const draft = profileDraft(profile, data.default_profile_id);
          draft.enabled = !draft.enabled;
		  const payload = profilePayload(draft);
		  if (Number(draft.config?.version) === 2) {
			const overview = await client.routingPolicy(profile.id);
			payload.expected_runtime_revision = Number(overview.runtime_state?.revision || 0);
		  }
          await runMutation(() =>
			client.updateProfile(profile.id, payload),
          );
        },
        delete: async (profile, replacementDefaultID) => {
          await runMutation(() =>
            client.deleteProfile(profile.id, replacementDefaultID),
          );
        },
      });
    } catch (error) {
      if (isUnauthorized(error)) {
        renderLoginScreen(root, client, generateProfile, path);
        return;
      }
      workspace.replaceChildren();
      showAppError(error);
    }
  }

  async function showOverview() {
    alert.hidden = true;
    alert.textContent = "";
    try {
      const data =
        typeof client.listProfiles === "function"
          ? await client.listProfiles()
          : { default_profile_id: 0, profiles: [] };
      await renderDashboard(workspace, {
        session,
        profiles: data,
        loadProfileStats: (profileId) =>
          typeof client.stats === "function"
            ? client.stats({ profile_id: profileId })
            : Promise.resolve({ summary: { requests: 0 } }),
      });
    } catch (error) {
      if (isUnauthorized(error)) {
        renderLoginScreen(root, client, generateProfile, path);
        return;
      }
      workspace.replaceChildren();
      showAppError(error);
    }
  }

  async function showProfileRoute() {
    try {
      if (route.section === "models" && typeof client.modelCatalog === "function") {
        try {
          installModelCatalog(await client.modelCatalog());
        } catch (error) {
          if (isUnauthorized(error)) {
            throw error;
          }
        }
      }
      const [profile, data] = await Promise.all([
        client.getProfile(route.profileId),
        client.listProfiles(),
      ]);
      heading.textContent = profile.display_name || profile.slug || "Profile";
      await showEditor(profileDraft(profile, data.default_profile_id));
    } catch (error) {
      if (isUnauthorized(error)) {
        renderLoginScreen(root, client, generateProfile, path);
        return;
      }
      if (error?.status === 404) {
        showNotFound();
        return;
      }
      workspace.replaceChildren();
      showAppError(error);
    }
  }

  async function showSetup() {
    heading.textContent = "开始配置";
    try {
      const data = await client.listProfiles();
      await renderOnboarding(workspace, {
        session,
        profiles: data,
        client,
      });
    } catch (error) {
      if (isUnauthorized(error)) {
        renderLoginScreen(root, client, generateProfile, path);
        return;
      }
      workspace.replaceChildren();
      showAppError(error);
    }
  }

  function showNotFound() {
    heading.textContent = "页面不存在";
    const card = document.createElement("section");
    card.className = "card empty-state";
    const title = document.createElement("h2");
    title.textContent = "页面不存在";
    const description = document.createElement("p");
    description.className = "muted";
    description.textContent = "地址可能已变更，或者 Profile 不存在。";
    const profiles = document.createElement("a");
    profiles.setAttribute("href", "/_admin/profiles");
    profiles.textContent = "返回 Profiles";
    card.append(title, description, profiles);
    workspace.replaceChildren(card);
  }

  async function showStats() {
    try {
      const data =
        typeof client.listProfiles === "function"
          ? await client.listProfiles()
          : { default_profile_id: 0, profiles: [] };
      await renderStatsPage(workspace, {
		section: route.section,
        profiles: data.profiles,
        loadStats: (filters) => client.stats(filters),
        loadRoutingTraces: (filters) =>
          typeof client.routingTraces === "function"
            ? client.routingTraces(filters)
			: Promise.resolve({ items: [], page: 1, page_size: 25, total: 0, total_pages: 0, category_counts: {} }),
		loadRoutingTrace: (id) =>
		  typeof client.routingTrace === "function"
			? client.routingTrace(id)
			: Promise.resolve({ trace: {}, candidates: [], calls: [] }),
		loadRoutingSessionFlow: (id) =>
		  typeof client.routingSessionFlow === "function"
			? client.routingSessionFlow(id)
			: Promise.resolve({
				scope: "request", selected_trace_id: Number(id), truncated: false, requests: [],
			}),
		loadModelPerformance: (filters) =>
		  typeof client.modelPerformance === "function"
			? client.modelPerformance(filters)
			: Promise.resolve({ items: [], page: 1, page_size: 25, total: 0, total_pages: 0, summary: {} }),
		loadEvaluationCatalog: () =>
		  typeof client.evaluationCatalog === "function"
			? client.evaluationCatalog()
			: Promise.resolve(null),
		loadAgentTrajectories: (filters) =>
		  typeof client.agentTrajectories === "function"
			? client.agentTrajectories(filters)
			: Promise.resolve({ items: [], page: 1, page_size: 25, total: 0, total_pages: 0, summary: {} }),
		loadAgentTrajectory: (id) =>
		  typeof client.agentTrajectory === "function"
			? client.agentTrajectory(id)
			: Promise.reject(new Error("轨迹详情不可用。")),
		deleteAgentTrajectory: (id) =>
		  typeof client.deleteAgentTrajectory === "function"
			? client.deleteAgentTrajectory(id)
			: Promise.reject(new Error("轨迹删除功能不可用。")),
        onUnauthorized: () =>
          renderLoginScreen(root, client, generateProfile, path),
      });
    } catch (error) {
      if (isUnauthorized(error)) {
        renderLoginScreen(root, client, generateProfile, path);
        return;
      }
      workspace.replaceChildren();
      showAppError(error);
    }
  }

  async function showSystem() {
    await renderSystemPage(workspace, {
      loadSystem: () => client.system(),
      loadProfiles: () => client.listProfiles(),
      loadModelCatalog: typeof client.modelCatalog === "function"
        ? () => client.modelCatalog()
        : undefined,
      refreshModelCatalog: typeof client.refreshModelCatalog === "function"
        ? () => client.refreshModelCatalog()
        : undefined,
      loadStrategies: typeof client.listStrategies === "function"
        ? (profileID) => client.listStrategies(profileID)
        : undefined,
      changePassword: (currentPassword, newPassword) =>
        client.changePassword(currentPassword, newPassword),
      onUnauthorized: () =>
        renderLoginScreen(root, client, generateProfile, path),
    });
  }

  logout.addEventListener("click", async () => {
    alert.hidden = true;
    alert.textContent = "";
    try {
      await client.logout();
      renderLoginScreen(root, client, generateProfile, path);
    } catch (error) {
      if (isUnauthorized(error)) {
        renderLoginScreen(root, client, generateProfile, path);
        return;
      }
      alert.textContent = error?.message || "请求失败，请重试。";
      alert.hidden = false;
    }
  });

  root.className = "app-shell";
  stage.append(
    createWindowFrame({
      titleText: "llm-proxy",
      className: "app-window",
      bodyClassName: "app-window-body",
      children: [sidebar, content],
    }),
    alert,
  );
  root.replaceChildren(stage);
  if (route.page === "overview") {
    await showOverview();
    return;
  }
  if (route.page === "setup") {
    await showSetup();
    return;
  }
  if (route.page === "profile") {
    await showProfileRoute();
    return;
  }
  if (route.page === "not-found") {
    showNotFound();
    return;
  }
  if (route.page === "stats") {
    await showStats();
    return;
  }
  if (route.page === "system") {
    await showSystem();
    return;
  }
  if (route.page === "help") {
    renderHelpPage(workspace, route.topic);
    return;
  }
  await showList();
}

function renderFatalError(root, error) {
  const stage = document.createElement("section");
  stage.className = "desktop-stage auth-stage auth-layout";
  const card = document.createElement("div");
  card.className = "card auth-card";
  const heading = document.createElement("h1");
  heading.textContent = "管理控制台";
  const alert = document.createElement("div");
  alert.className = "error-banner";
  alert.setAttribute("role", "alert");
  alert.textContent = error?.message || "请求失败，请重试。";
  alert.hidden = false;
  card.append(heading, alert);
  stage.append(
    createWindowFrame({
      titleText: "llm-proxy",
      className: "auth-window",
      children: [card],
    }),
  );
  root.className = "";
  root.replaceChildren(stage);
}

function isUnauthorized(error) {
  return error?.status === 401;
}

function currentPath() {
  if (typeof window === "undefined") {
    return "/_admin/";
  }
  return window.location.pathname;
}

function navigationPage(route) {
  if (route.page === "profile") {
    return "profiles";
  }
  if (route.page === "setup") {
    return "overview";
  }
  return route.page;
}

function navigateTo(href) {
  if (typeof window !== "undefined") {
    window.location.assign(href);
  }
}

if (typeof window !== "undefined" && typeof document !== "undefined") {
  void bootstrap();
}
