import { renderPasswordChangePanel } from "./auth.js";
import { installModelCatalog } from "./model-catalog.js";
import { analyzeCatalogImpact } from "./model-catalog-impact.js";

export function buildProfileExport(data = {}) {
  const defaultProfileID = Number(data.default_profile_id ?? 0);
  return {
    version: 1,
    profiles: (data.profiles || []).map((profile) => ({
      slug: String(profile.slug ?? ""),
      display_name: String(profile.display_name ?? ""),
      enabled: Boolean(profile.enabled),
      default: Number(profile.id) === defaultProfileID,
      config: profile.config,
    })),
  };
}

export async function renderSystemPage(
  root,
  {
    loadSystem,
    loadProfiles,
    loadModelCatalog,
    refreshModelCatalog,
    loadStrategies = async () => ({ strategies: [] }),
    changePassword,
    download = downloadJSON,
    onUnauthorized = () => {},
  },
) {
  root.replaceChildren(statusMessage("正在加载系统信息…"));

  let system;
  let catalog = null;
  try {
    [system, catalog] = await Promise.all([
      loadSystem(),
      typeof loadModelCatalog === "function"
        ? loadModelCatalog().catch(() => null)
        : Promise.resolve(null),
    ]);
  } catch (error) {
    if (error?.status === 401) {
      onUnauthorized();
      return;
    }
    root.replaceChildren(alertMessage(error?.message || "请求失败，请重试。"));
    return;
  }

  const page = element("div", "stack system-page");
  const information = element("section", "card");
  information.append(textElement("h2", "系统信息"));
  const values = element("dl", "system-values");
  for (const [label, value] of [
    ["版本", system.version],
    ["数据目录", system.data_dir],
    ["数据库文件", system.database_file],
    ["数据库大小", `${Number(system.database_bytes ?? 0)} B`],
    ["Schema 版本", Number(system.schema_version ?? 0)],
    ["默认 Profile ID", Number(system.default_profile_id ?? 0)],
    ["必须修改密码", system.password_must_change ? "是" : "否"],
  ]) {
    values.append(
      textElement("dt", label),
      textElement("dd", `${label}：${value}`),
    );
  }
  information.append(values);

  const catalogSection = element("section", "card stack model-catalog-system");
  catalogSection.append(
    textElement("h2", "模型信息库"),
    textElement(
      "p",
      "只更新后台推荐信息，不会修改任何 Profile 或线上策略。",
      "muted",
    ),
  );
  const catalogDetails = element("div", "model-catalog-summary");
  const catalogAlert = element("div", "model-catalog-refresh-message");
  catalogAlert.setAttribute("role", "status");
  catalogAlert.setAttribute("aria-live", "polite");
  catalogAlert.hidden = true;
  const impactOutput = element("div", "catalog-impact-output stack");
  const refreshCatalog = textElement("button", "从 Models.dev 更新");
  refreshCatalog.type = "button";
  refreshCatalog.className = "button";
  refreshCatalog.disabled = !catalog || typeof refreshModelCatalog !== "function";

  const renderCatalogDetails = () => {
    if (!catalog) {
      catalogDetails.replaceChildren(
        textElement("p", "当前无法读取模型信息库。", "muted"),
      );
      return;
    }
    catalogDetails.replaceChildren(
      textElement("p", `来源：${catalog.source?.name || "未知"}`),
      textElement("p", `${catalog.models?.length || 0} 个模型`),
      textElement("p", `Revision：${String(catalog.source?.revision || "").slice(0, 12)}`),
      textElement("p", `获取日期：${catalog.source?.retrieved || "未知"}`),
    );
  };
  renderCatalogDetails();

  refreshCatalog.addEventListener("click", async () => {
    const previous = catalog;
    catalogAlert.hidden = false;
    catalogAlert.className = "model-catalog-refresh-message muted";
    catalogAlert.textContent = "正在下载并校验远程模型信息库…";
    refreshCatalog.disabled = true;
    impactOutput.replaceChildren();
    let result;
    try {
      result = await refreshModelCatalog();
      catalog = result.catalog;
      installModelCatalog(catalog);
      renderCatalogDetails();
      if (!result.changed) {
        catalogAlert.textContent = "当前已经是最新版本。";
        return;
      }

      catalogAlert.textContent = "模型信息库已更新；已保存的 Profile 参数没有变化。";
      try {
      const profileData = await loadProfiles();
      const strategiesByProfile = {};
      await Promise.all((profileData.profiles || []).map(async (profile) => {
        strategiesByProfile[profile.id] = await loadStrategies(profile.id);
      }));
      const impact = analyzeCatalogImpact({
        previous,
        current: catalog,
        profiles: profileData.profiles || [],
        strategiesByProfile,
      });
      renderImpactReport(impactOutput, impact);
      } catch (error) {
        catalogAlert.className = "warning-banner model-catalog-refresh-message";
        catalogAlert.textContent = `模型信息库已更新，但影响分析失败：${
          error?.message || "请稍后重试。"
        }`;
      }
    } catch (error) {
      catalog = previous;
      renderCatalogDetails();
      catalogAlert.className = "error-banner model-catalog-refresh-message";
      catalogAlert.textContent = error?.message || "模型信息库更新失败。";
    } finally {
      refreshCatalog.disabled = false;
    }
  });
  catalogSection.append(
    catalogDetails,
    catalogAlert,
    refreshCatalog,
    impactOutput,
  );

  const backup = element("section", "card stack");
  backup.append(
    textElement("h2", "Profile 备份"),
    textElement(
      "p",
      "导出只包含可移植的 Profile 配置，不包含统计、认证或会话数据。",
    ),
  );
  const exportAlert = element("div", "error-banner");
  exportAlert.setAttribute("role", "alert");
  exportAlert.hidden = true;
  const exportButton = textElement("button", "导出 Profiles");
  exportButton.type = "button";
  exportButton.className = "button";
  exportButton.addEventListener("click", async () => {
    exportAlert.hidden = true;
    exportAlert.textContent = "";
    exportButton.disabled = true;
    try {
      const profiles = await loadProfiles();
      download(
        "llm-proxy-profiles-v1.json",
        JSON.stringify(buildProfileExport(profiles), null, 2),
      );
    } catch (error) {
      if (error?.status === 401) {
        onUnauthorized();
        return;
      }
      exportAlert.textContent = error?.message || "导出失败，请重试。";
      exportAlert.hidden = false;
    } finally {
      exportButton.disabled = false;
    }
  });
  backup.append(exportAlert, exportButton);

  const password = element("section", "system-password");
  renderPasswordChangePanel(password, {
    changePassword: async (...values) => {
      try {
        await changePassword(...values);
      } catch (error) {
        if (error?.status === 401) {
          onUnauthorized();
          return;
        }
        throw error;
      }
    },
  });

  page.append(information, catalogSection, backup, password);
  root.replaceChildren(page);
}

export function downloadJSON(filename, contents) {
  const url = URL.createObjectURL(
    new Blob([contents], { type: "application/json;charset=utf-8" }),
  );
  const link = document.createElement("a");
  link.href = url;
  link.download = filename;
  link.click();
  URL.revokeObjectURL(url);
}

function statusMessage(text) {
  const status = textElement("p", text);
  status.className = "muted";
  status.setAttribute("role", "status");
  return status;
}

function alertMessage(text) {
  const alert = textElement("div", text);
  alert.className = "error-banner";
  alert.setAttribute("role", "alert");
  return alert;
}

function element(tagName, className = "") {
  const result = document.createElement(tagName);
  result.className = className;
  return result;
}

function textElement(tagName, text, className = "") {
  const result = element(tagName, className);
  result.textContent = String(text);
  return result;
}

function renderImpactReport(root, rows) {
  root.append(textElement("h3", "潜在影响报告"));
  if (rows.length === 0) {
    root.append(textElement("p", "没有已保存配置引用本次变化的模型。", "muted"));
    return;
  }
  for (const row of rows) {
    const item = element("article", `catalog-impact-row impact-${row.risk}`);
    const header = element("div", "cluster");
    header.append(
      textElement("span", riskLabel(row.risk), `badge impact-badge-${row.risk}`),
      textElement("strong", row.profileName),
      textElement("code", row.modelId),
    );
    const scope = row.strategy
      ? `${row.label} · ${row.strategy.name}（${strategyStateLabel(row.strategy.state)}）`
      : row.label;
    const link = textElement("a", "打开相关设置");
    link.setAttribute("href", row.href);
    item.append(
      header,
      textElement("p", scope),
      textElement("p", row.changes.map((change) => change.label).join("、"), "muted"),
      link,
    );
    root.append(item);
  }
}

function riskLabel(risk) {
  return { high: "高风险", medium: "中风险", low: "低风险" }[risk] || "提示";
}

function strategyStateLabel(state) {
  return {
    active: "正式",
    canary: "灰度",
    evaluating: "评估中",
    ready: "待灰度",
    draft: "草稿",
  }[state] || state;
}
