import { api } from "./api.js";
import {
  nextScreen,
  renderLogin,
  renderPasswordChange,
} from "./auth.js";
import { createWindowFrame } from "./chrome.js";
import { renderDashboard } from "./dashboard.js";
import {
  defaultProfileDraft,
  profileDraft,
  profilePayload,
  renderProfileEditor,
  renderProfileList,
} from "./profiles.js";
import { renderStatsPage } from "./stats.js";
import { renderSystemPage } from "./system.js";
import { parseAdminRoute } from "./routes.js";

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
  await renderAuthenticated(root, client, session, generateProfile, path);
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
    "not-found": "页面不存在",
  }[pageName];
  const alert = document.createElement("div");
  alert.className = "error-banner";
  alert.setAttribute("role", "alert");
  alert.hidden = true;
  const workspace = document.createElement("div");
  workspace.className = "profile-workspace";
  headingGroup.append(heading);
  content.append(headingGroup, alert, workspace);

  function showAppError(error) {
    alert.textContent = error?.message || "请求失败，请重试。";
    alert.hidden = false;
  }

  async function runMutation(mutation) {
    try {
      await mutation();
      await showList();
    } catch (error) {
      if (isUnauthorized(error)) {
        renderLoginScreen(root, client, generateProfile, path);
        return;
      }
      throw error;
    }
  }

  async function showEditor(draft, editingStrategyID = 0) {
    alert.hidden = true;
    alert.textContent = "";
    let strategyOverview = null;
    if (
      draft.id &&
      draft.config?.auto_routing?.enabled &&
      typeof client.listStrategies === "function"
    ) {
      strategyOverview = await client.listStrategies(draft.id);
      if (editingStrategyID) {
        const editing = (strategyOverview.strategies || []).find(
          (version) => Number(version.id) === Number(editingStrategyID),
        );
        if (editing) {
          draft.config.auto_routing.strategy = structuredClone(editing.config);
        } else {
          editingStrategyID = 0;
        }
      }
    }
    draft.strategy_overview = strategyOverview;
    draft.strategy_editing_id = editingStrategyID;

    async function reloadStrategyEditor(nextEditingID = 0) {
      const profile = await client.getProfile(draft.id);
      await showEditor(
        profileDraft(profile, draft.make_default ? profile.id : 0),
        nextEditingID,
      );
    }

    renderProfileEditor(workspace, draft, {
      save: async (payload) => {
        await runMutation(async () => {
          if (draft.id) {
            await client.updateProfile(draft.id, payload);
            return;
          }
          await client.createProfile(payload);
        });
      },
      cancel: () => {
        void showList();
      },
      generate: generateProfile,
      createStrategy: async (config) => {
        const version = await client.createStrategy(draft.id, config);
        await reloadStrategyEditor(version.id);
      },
      updateStrategy: async (strategyID, config) => {
        await client.updateStrategy(draft.id, strategyID, config);
        await reloadStrategyEditor(strategyID);
      },
      editStrategy: (strategyID) => reloadStrategyEditor(strategyID),
      advanceStrategy: async (strategyID, from, to) => {
        await client.advanceStrategy(draft.id, strategyID, from, to);
        await reloadStrategyEditor(to === "evaluating" ? strategyID : 0);
      },
      startStrategyCanary: async (strategyID, canaryBps, revision) => {
        await client.startStrategyCanary(
          draft.id,
          strategyID,
          canaryBps,
          revision,
        );
        await reloadStrategyEditor();
      },
      cancelStrategyCanary: async (revision) => {
        await client.cancelStrategyCanary(draft.id, revision);
        await reloadStrategyEditor();
      },
      promoteStrategy: async (revision) => {
        await client.promoteStrategy(draft.id, revision);
        await reloadStrategyEditor();
      },
      rollbackStrategy: async (revision) => {
        await client.rollbackStrategy(draft.id, revision);
        await reloadStrategyEditor();
      },
		generateStrategyCandidate: async () => {
			const version = await client.generateStrategyCandidate(draft.id);
			await reloadStrategyEditor(version.id);
		},
    });
  }

  async function openEditor(draft, editingStrategyID = 0) {
    try {
      await showEditor(draft, editingStrategyID);
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
          void openEditor(draft);
        },
        edit: (profile) =>
          void openEditor(profileDraft(profile, data.default_profile_id)),
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
          await runMutation(() =>
            client.updateProfile(profile.id, profilePayload(draft)),
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

  function showSetupPlaceholder() {
    const card = document.createElement("section");
    card.className = "card empty-state";
    const title = document.createElement("h2");
    title.textContent = "开始配置";
    const description = document.createElement("p");
    description.className = "muted";
    description.textContent = "正在准备首次接入引导。";
    const profiles = document.createElement("a");
    profiles.href = "/_admin/profiles";
    profiles.textContent = "前往 Profiles";
    card.append(title, description, profiles);
    workspace.replaceChildren(card);
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
        profiles: data.profiles,
        loadStats: (filters) => client.stats(filters),
        loadRoutingTraces: (filters) =>
          typeof client.routingTraces === "function"
            ? client.routingTraces(filters)
            : Promise.resolve([]),
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
  );
  root.replaceChildren(stage);
  if (route.page === "overview") {
    await showOverview();
    return;
  }
  if (route.page === "setup") {
    showSetupPlaceholder();
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

if (typeof window !== "undefined" && typeof document !== "undefined") {
  void bootstrap();
}
