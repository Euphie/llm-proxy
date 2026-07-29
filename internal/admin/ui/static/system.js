import { renderPasswordChangePanel } from "./auth.js";

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
    changePassword,
    download = downloadJSON,
    onUnauthorized = () => {},
  },
) {
  root.replaceChildren(statusMessage("正在加载系统信息…"));

  let system;
  try {
    system = await loadSystem();
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

  page.append(information, backup, password);
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

function textElement(tagName, text) {
  const result = element(tagName);
  result.textContent = String(text);
  return result;
}
