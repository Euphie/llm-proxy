import { api } from "./api.js";
import { renderAggregateGatewaysPage } from "./aggregate-gateways.js";
import {
  nextScreen,
  renderLogin,
  renderPasswordChange,
} from "./auth.js";
import { createWindowFrame } from "./chrome.js";
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
  const stage = document.createElement("div");
  stage.className = "desktop-stage";
  const sidebar = document.createElement("aside");
  sidebar.className = "app-sidebar";
  const brand = document.createElement("div");
  brand.className = "app-brand";
  const brandMark = document.createElement("span");
  brandMark.className = "app-brand-mark";
  brandMark.textContent = "LP";
  brandMark.setAttribute("aria-hidden", "true");
  const brandCopy = document.createElement("span");
  brandCopy.className = "app-brand-copy";
  const brandName = document.createElement("strong");
  brandName.className = "app-brand-name";
  brandName.textContent = "LLM Proxy";
  const brandMeta = document.createElement("span");
  brandMeta.className = "app-brand-meta";
  brandMeta.textContent = "管理控制台";
  brandCopy.append(brandName, brandMeta);
  brand.append(brandMark, brandCopy);
  const navigation = document.createElement("nav");
  navigation.setAttribute("aria-label", "主导航");
  const list = document.createElement("ul");
  list.className = "app-nav";

  for (const section of navigationSections()) {
    if (section.items) {
      list.append(navigationGroup(section, pageName));
      continue;
    }
    list.append(navigationItem(section, pageName));
  }

  const logoutEntry = document.createElement("li");
  logoutEntry.className = "app-nav-logout";
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
  heading.textContent = pageTitle(pageName);
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

  function showEditor(draft, aggregateGateways = []) {
    alert.hidden = true;
    alert.textContent = "";
    renderProfileEditor(workspace, draft, {
      aggregateGateways,
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
      const [data, gatewayData] = await Promise.all([
        typeof client.listProfiles === "function"
          ? client.listProfiles()
          : { default_profile_id: 0, profiles: [] },
        typeof client.listAggregateGateways === "function"
          ? client.listAggregateGateways()
          : { gateways: [] },
      ]);
      const aggregateGateways = gatewayData?.gateways || [];
      renderProfileList(workspace, {
        ...data,
        aggregate_gateways: aggregateGateways,
      }, {
        create: () => {
          const draft = defaultProfileDraft();
          draft.make_default = data.profiles.length === 0;
          showEditor(draft, aggregateGateways);
        },
        edit: (profile) =>
          showEditor(profileDraft(profile, data.default_profile_id), aggregateGateways),
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
      const [data, gatewayData] = await Promise.all([
        typeof client.listProfiles === "function"
          ? client.listProfiles()
          : Promise.resolve({ default_profile_id: 0, profiles: [] }),
        typeof client.listAggregateGateways === "function"
          ? client.listAggregateGateways()
          : Promise.resolve({ gateways: [] }),
      ]);
      await renderStatsPage(workspace, {
        profiles: data.profiles,
        gateways: gatewayData.gateways,
        loadGatewayKeys: (gatewayID) =>
          typeof client.listAggregateGatewayKeys === "function"
            ? client.listAggregateGatewayKeys(gatewayID)
            : Promise.resolve({ keys: [] }),
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

  async function showAggregateGateways(activePageName, notice = "") {
    alert.hidden = true;
    alert.textContent = "";
    try {
      const [providerData, gatewayData] = await Promise.all([
        client.listProviderAccounts?.() ?? { providers: [] },
        client.listAggregateGateways?.() ?? { gateways: [] },
      ]);
      const gateways = gatewayData?.gateways || [];
      const keyLists = await Promise.all(
        gateways.map((gateway) =>
          client.listAggregateGatewayKeys?.(gateway.id) ?? { keys: [] },
        ),
      );
      renderAggregateGatewaysPage(workspace, {
        providers: providerData?.providers || [],
        gateways,
        keys: keyLists.flatMap((list) => list?.keys || []),
      }, {
        saveProvider: async (payload, providerID = 0) => {
          const updating = providerID > 0;
          if (providerID > 0) {
            await client.updateProviderAccount(providerID, payload);
          } else {
            await client.createProviderAccount(payload);
          }
          await showAggregateGateways(
            activePageName,
            updating ? "供应商更新成功。" : "供应商创建成功。",
          );
        },
        saveGateway: async (payload, gatewayID = 0) => {
          const updating = gatewayID > 0;
          if (gatewayID > 0) {
            await client.updateAggregateGateway(gatewayID, payload);
          } else {
            await client.createAggregateGateway(payload);
          }
          await showAggregateGateways(
            activePageName,
            updating ? "网关更新成功。" : "网关创建成功。",
          );
        },
        createKey: (gatewayID, payload) =>
          client.createAggregateGatewayKey(gatewayID, payload),
      }, {
        view: aggregateViewForPage(activePageName),
        gatewayID: gatewayIDForPath(path),
        notice,
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
      titleText: "",
      className: "app-window",
      bodyClassName: "app-window-body",
      children: [sidebar, content],
    }),
  );
  root.replaceChildren(stage);
  if (pageName === "stats") {
    await showStats();
    return;
  }
  if (isAggregatePage(pageName)) {
    await showAggregateGateways(pageName);
    return;
  }
  if (pageName === "system") {
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
    return "/_admin/profiles";
  }
  return `${window.location.pathname}${window.location.search || ""}`;
}

function navigationSections() {
  return [
    {
      name: "profiles",
      text: "代理通道",
      href: "/_admin/profiles",
      iconClass: "nav-icon-blue",
      iconText: "P",
    },
    {
      name: "aggregate",
      text: "聚合网关",
      iconClass: "nav-icon-green",
      iconText: "G",
      items: [
        {
          name: "aggregate-providers",
          text: "供应商管理",
          href: "/_admin/aggregate-gateways/providers",
        },
        {
          name: "aggregate-gateway-list",
          text: "网关管理",
          href: "/_admin/aggregate-gateways",
        },
        {
          name: "aggregate-gateways",
          text: "网关管理",
          href: "/_admin/aggregate-gateways/gateways",
          hidden: true,
        },
        {
          name: "aggregate-keys",
          text: "秘钥管理",
          href: "/_admin/aggregate-gateways/keys",
        },
      ],
    },
    {
      name: "stats",
      text: "用量统计",
      href: "/_admin/stats",
      iconClass: "nav-icon-orange",
      iconText: "S",
    },
    {
      name: "system",
      text: "系统设置",
      href: "/_admin/system",
      iconClass: "nav-icon-gray",
      iconText: "⚙",
    },
  ];
}

function navigationItem(item, pageName) {
  const entry = document.createElement("li");
  entry.className = "app-nav-section";
  const link = document.createElement("a");
  link.className = "app-nav-primary";
  link.textContent = item.text;
  link.setAttribute("href", item.href);
  link.append(navigationIcon(item));
  if (item.name === pageName) {
    link.setAttribute("aria-current", "page");
  }
  entry.append(link);
  return entry;
}

function navigationGroup(group, pageName) {
  const entry = document.createElement("li");
  entry.className = "app-nav-section";
  const active = group.items.some((item) => item.name === pageName);
  const parent = document.createElement("div");
  parent.className = active
    ? "app-nav-primary app-nav-parent app-nav-parent-active"
    : "app-nav-primary app-nav-parent";
  parent.textContent = group.text;
  parent.append(navigationIcon(group));

  const children = document.createElement("ul");
  children.className = "app-nav-children";
  for (const item of group.items) {
    if (item.hidden) {
      continue;
    }
    const child = document.createElement("li");
    const link = document.createElement("a");
    link.className = "app-nav-child";
    link.textContent = item.text;
    link.setAttribute("href", item.href);
    if (item.name === pageName) {
      link.setAttribute("aria-current", "page");
    }
    child.append(link);
    children.append(child);
  }
  entry.append(parent, children);
  return entry;
}

function navigationIcon(item) {
  const icon = document.createElement("span");
  icon.className = `app-nav-icon ${item.iconClass}`;
  icon.textContent = item.iconText;
  icon.setAttribute("aria-hidden", "true");
  return icon;
}

function pageTitle(pageName) {
  return {
    profiles: "代理通道",
    "aggregate-providers": "供应商管理",
    "aggregate-gateway-list": "网关管理",
    "aggregate-gateways": "网关管理",
    "aggregate-keys": "秘钥管理",
    stats: "用量统计",
    system: "系统设置",
  }[pageName] || "代理通道";
}

function isAggregatePage(pageName) {
  return [
    "aggregate-providers",
    "aggregate-gateway-list",
    "aggregate-gateways",
    "aggregate-keys",
  ].includes(pageName);
}

function aggregateViewForPage(pageName) {
  return {
    "aggregate-providers": "providers",
    "aggregate-gateway-list": "gateway-list",
    "aggregate-gateways": "gateways",
    "aggregate-keys": "keys",
  }[pageName] || "gateways";
}

function pageForPath(path) {
  path = String(path || "").split("?", 1)[0];
  if (path === "/_admin/aggregate-gateways/providers") {
    return "aggregate-providers";
  }
  if (
    path === "/_admin/aggregate-gateways" ||
    path === "/_admin/aggregate-gateways/" ||
    path === "/_admin/aggregate-gateways/list"
  ) {
    return "aggregate-gateway-list";
  }
  if (path === "/_admin/aggregate-gateways/gateways") {
    return "aggregate-gateways";
  }
  if (path === "/_admin/aggregate-gateways/keys") {
    return "aggregate-keys";
  }
  if (path === "/_admin/stats") {
    return "stats";
  }
  if (path === "/_admin/system") {
    return "system";
  }
  return "profiles";
}

function gatewayIDForPath(path) {
  const query = String(path || "").split("?", 2)[1] || "";
  const gatewayID = Number(new URLSearchParams(query).get("id"));
  return Number.isSafeInteger(gatewayID) && gatewayID > 0 ? gatewayID : 0;
}

if (typeof window !== "undefined" && typeof document !== "undefined") {
  void bootstrap();
}
