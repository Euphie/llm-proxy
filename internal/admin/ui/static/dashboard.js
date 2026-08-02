import { profileDraft } from "./profile-draft.js";
import { profileReadiness } from "./profile-readiness.js";
import { profileSectionHref } from "./routes.js";

export async function renderDashboard(root, {
  session = {},
  profiles: profileData = {},
  loadProfileStats = async () => ({ summary: { requests: 0 } }),
} = {}) {
  const profiles = profileData.profiles || [];
  const page = element("div", "dashboard stack");

  if (profiles.length === 0) {
    const onboarding = element("section", "card onboarding-card stack");
    onboarding.append(
      textElement("p", "开始使用", "eyebrow"),
      textElement("h2", "欢迎使用 llm-proxy"),
      textElement(
        "p",
        "先连接一个上游，然后生成 Agent 配置并发送首个真实请求。",
        "muted",
      ),
      linkElement("开始配置", "/_admin/setup", "button"),
    );
    page.append(onboarding, quickGuide());
    root.replaceChildren(page);
    return;
  }

  const primary = selectPrimaryProfile(profiles, profileData.default_profile_id);
  const stats = await loadProfileStats(primary.id);
  const requestCount = Number(stats?.summary?.requests || 0);
  const firstRequestPending = requestCount === 0;

  if (firstRequestPending) {
    const onboarding = element("section", "card onboarding-card stack");
    onboarding.append(
      textElement("p", "还差一步", "eyebrow"),
      textElement("h2", "等待首个请求"),
      textElement(
        "p",
        "Profile 已保存。生成 Agent 配置并发出一个真实请求，即可完成初始化。",
        "muted",
      ),
      linkElement(
        "生成 Agent 配置",
        profileSectionHref(primary.id, "agents"),
        "button",
      ),
    );
    page.append(onboarding);
  } else {
    const status = element("section", "dashboard-status cluster");
    status.append(
      textElement("strong", "运行正常"),
      textElement("span", `${requestCount} 次请求`, "status-pill status-ready"),
    );
    page.append(status);
  }

  const summary = element("section", "dashboard-section stack");
  const summaryHeader = element("div", "cluster section-heading");
  summaryHeader.append(
    textElement("h2", "Profiles"),
    linkElement("管理全部", "/_admin/profiles"),
  );
  const cards = element("div", "dashboard-profile-grid");
  for (const profile of profiles) {
    cards.append(profileCard(profile, profileData.default_profile_id));
  }
  summary.append(summaryHeader, cards);
  page.append(summary);

  const footer = element("section", "dashboard-links cluster");
  footer.append(
    linkElement("查看统计", "/_admin/stats"),
    textElement("span", `当前管理员：${session.username || "admin"}`, "muted"),
  );
  page.append(footer);
  root.replaceChildren(page);
}

function profileCard(profile, defaultProfileID) {
  const draft = profileDraft(profile, defaultProfileID);
  const readiness = profileReadiness(draft, { saved: true });
  const card = element("article", "card dashboard-profile-card stack");
  const header = element("div", "cluster section-heading");
  header.append(
    textElement("h3", profile.display_name || profile.slug),
    textElement(
      "span",
      profile.enabled ? "已启用" : "已停用",
      `status-pill ${profile.enabled ? "status-ready" : "status-disabled"}`,
    ),
  );
  const proxyPath = `/${profile.slug}/v1/messages`;
  const detail = element("div", "dashboard-profile-details stack");
  detail.append(
    textElement("code", proxyPath),
    textElement(
      "p",
      readiness.state === "ready" ? "基础配置可用" : "仍有配置需要处理",
      "muted",
    ),
  );
  card.append(
    header,
    detail,
    linkElement("打开 Profile", profileSectionHref(profile.id, "overview")),
  );
  return card;
}

function quickGuide() {
  const section = element("section", "dashboard-section stack");
  section.append(textElement("h2", "三步接入"));
  const list = element("ol", "setup-step-list");
  for (const [title, description] of [
    ["连接上游", "填写协议、Profile 地址和上游基础地址。"],
    ["生成配置", "选择 Claude Code、Codex 或 OpenCode，并复制生成结果。"],
    ["验证请求", "从 Agent 发出真实请求，后台会自动识别。"],
  ]) {
    const item = element("li", "setup-step");
    item.append(textElement("strong", title), textElement("p", description, "muted"));
    list.append(item);
  }
  section.append(list);
  return section;
}

function selectPrimaryProfile(profiles, defaultProfileID) {
  return profiles.find((profile) => Number(profile.id) === Number(defaultProfileID)) ||
    profiles.find((profile) => profile.enabled) || profiles[0];
}

function element(tag, className = "") {
  const value = document.createElement(tag);
  value.className = className;
  return value;
}

function textElement(tag, text, className = "") {
  const value = element(tag, className);
  value.textContent = text;
  return value;
}

function linkElement(text, href, className = "") {
  const link = textElement("a", text, className);
  link.setAttribute("href", href);
  return link;
}
