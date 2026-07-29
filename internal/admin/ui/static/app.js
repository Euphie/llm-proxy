import { api } from "./api.js";
import {
  nextScreen,
  renderLogin,
  renderPasswordChange,
} from "./auth.js";
import {
  defaultProfileDraft,
  profileDraft,
  profilePayload,
  renderProfileEditor,
  renderProfileList,
} from "./profiles.js";
import { renderStatsPage } from "./stats.js";
import { renderSystemPage } from "./system.js";

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
  await renderAuthenticated(root, client, generateProfile, path);
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

async function renderAuthenticated(root, client, generateProfile, path) {
  const pageName = pageForPath(path);
  const header = document.createElement("header");
  header.className = "app-header";
  const brand = document.createElement("p");
  brand.className = "app-brand";
  brand.textContent = "llm-proxy";
  const navigation = document.createElement("nav");
  navigation.setAttribute("aria-label", "主导航");
  const list = document.createElement("ul");
  list.className = "app-nav";

  for (const item of [
    { name: "profiles", text: "Profiles", href: "/_admin/profiles" },
    { name: "stats", text: "统计", href: "/_admin/stats" },
    { name: "system", text: "系统", href: "/_admin/system" },
  ]) {
    const entry = document.createElement("li");
    const link = document.createElement("a");
    link.textContent = item.text;
    link.setAttribute("href", item.href);
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
  header.append(brand, navigation);

  const content = document.createElement("section");
  content.className = "app-content";
  const headingGroup = document.createElement("div");
  headingGroup.className = "page-heading";
  const heading = document.createElement("h1");
  heading.textContent = {
    profiles: "Profiles",
    stats: "统计",
    system: "系统",
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

  function showEditor(draft) {
    alert.hidden = true;
    alert.textContent = "";
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
    });
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
          showEditor(draft);
        },
        edit: (profile) =>
          showEditor(profileDraft(profile, data.default_profile_id)),
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

  async function showStats() {
    try {
      const data =
        typeof client.listProfiles === "function"
          ? await client.listProfiles()
          : { default_profile_id: 0, profiles: [] };
      await renderStatsPage(workspace, {
        profiles: data.profiles,
        loadStats: (filters) => client.stats(filters),
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
  root.replaceChildren(header, content);
  if (pageName === "stats") {
    await showStats();
    return;
  }
  if (pageName === "system") {
    await showSystem();
    return;
  }
  await showList();
}

function renderFatalError(root, error) {
  const layout = document.createElement("section");
  layout.className = "auth-layout";
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
  layout.append(card);
  root.className = "";
  root.replaceChildren(layout);
}

function isUnauthorized(error) {
  return error?.status === 401;
}

function currentPath() {
  if (typeof window === "undefined") {
    return "/_admin/profiles";
  }
  return window.location.pathname;
}

function pageForPath(path) {
  if (path === "/_admin/stats") {
    return "stats";
  }
  if (path === "/_admin/system") {
    return "system";
  }
  return "profiles";
}

if (typeof window !== "undefined" && typeof document !== "undefined") {
  void bootstrap();
}
