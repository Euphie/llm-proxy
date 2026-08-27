import { renderProfileEditor as renderLegacyProfileEditor } from "./profiles.js";

const steps = [
  ["connection", "连接上游", "基础配置"],
  ["models", "录入模型", "模型能力"],
  ["routing", "智能路由", "智能路由"],
  ["complete", "完成", ""],
];

export async function renderProfileCreationWizard(root, source, {
  createProfile,
  updateProfile,
  generateStrategy,
  loadEvaluationCatalog,
  updateEvaluationCatalog,
  onSaved = () => {},
  cancel = () => {},
  complete = () => {},
  renderEditor = renderLegacyProfileEditor,
} = {}) {
  let profile = structuredClone(source);
  let stepIndex = 0;

  async function render() {
    const [step, , heading] = steps[stepIndex];
    if (step === "complete") {
      renderComplete(root, profile, complete);
      return;
    }
    if (step === "routing" && typeof loadEvaluationCatalog === "function") {
      try {
        profile.evaluation_catalog = await loadEvaluationCatalog();
      } catch {
        profile.evaluation_catalog = null;
      }
    }

    const shell = element("div", "profile-create-wizard stack");
    const header = element("header", "profile-create-header cluster");
    const title = element("div", "stack");
    title.append(
      textElement("p", "新建 Profile", "eyebrow"),
      textElement("h1", steps[stepIndex][1]),
    );
    const exit = actionButton("退出向导", "button-secondary");
    exit.addEventListener("click", () => cancel(profile));
    header.append(title, exit);
    shell.append(header, stepNavigation(stepIndex));

    const mount = element("div", "profile-create-panel");
    const editorActions = {
      save: async (payload) => {
        const completesWizard = step === "routing";
        if (step === "connection") {
          const created = await createProfile?.(payload);
          profile = normalizedProfile(created, payload);
          stepIndex = 1;
        } else {
          const updated = await updateProfile?.(profile.id, payload);
          profile = normalizedProfile(updated, { ...payload, id: profile.id });
          stepIndex = step === "models" ? 2 : 3;
        }
        await render();
        if (completesWizard) {
          onSaved("Profile 保存成功。", profile);
        }
      },
      cancel: async () => {
        if (stepIndex === 0) {
          cancel(profile);
          return;
        }
        stepIndex -= 1;
        await render();
      },
      generate: () => {},
    };
    if (typeof generateStrategy === "function") {
      editorActions.generateStrategy = async (intent) => {
        const generated = await generateStrategy(profile.id, intent);
        if (generated?.recommendation) {
          profile = applyRecommendation(profile, generated.recommendation);
        }
        await render();
        return generated;
      };
    }
    if (typeof updateEvaluationCatalog === "function") {
		editorActions.updateEvaluationCatalog = async (onProgress) => {
			const job = await updateEvaluationCatalog(onProgress);
			await render();
			return job;
		};
    }
    renderEditor(mount, profile, editorActions);

    const form = findDescendant(mount, (item) => item.tagName === "FORM");
    if (!form) {
      throw new Error("Profile 创建向导未生成表单。");
    }
    retainSection(form, heading);
    configureFooter(form, step, async () => {
      stepIndex = step === "models" ? 2 : 3;
      await render();
    });
    shell.append(mount);
    root.replaceChildren(shell);
  }

  await render();
}

function applyRecommendation(source, recommendation) {
  const profile = structuredClone(source);
  const roles = recommendation.roles || {};
  profile.config.auto_routing = {
    ...profile.config.auto_routing,
    enabled: true,
    participants: [...(roles.participants || [])],
    strong_baseline_model: roles.strong_baseline_model || "",
    task_analyzer_model: roles.task_analyzer_model || "",
    dynamic_optimization: {
      ...profile.config.auto_routing.dynamic_optimization,
      reviewer_model: roles.reviewer_model || "",
      daily_budget_micro_usd:
        roles.daily_eval_budget_micro_usd ||
        profile.config.auto_routing.dynamic_optimization.daily_budget_micro_usd,
    },
    strategy: structuredClone(recommendation.config),
  };
  profile.strategy_configuration_mode = "manual";
  profile.strategy_generation_notice = "推荐草稿已生成，可以直接调整后保存。";
  return profile;
}

function normalizedProfile(response, fallback) {
  const saved = response && typeof response === "object" ? response : fallback;
  const profile = {
    ...structuredClone(fallback),
    ...structuredClone(saved),
    config: structuredClone(saved?.config || fallback.config),
  };
  profile.id = Number(saved?.id || fallback.id || 0);
  profile.original_slug = String(profile.slug || "");
  profile.make_default = Boolean(fallback.make_default);
  return profile;
}

function retainSection(form, heading) {
  for (const child of [...form.children]) {
    if (child.tagName !== "SECTION") continue;
    const title = Array.from(child.children).find((item) => item.tagName === "H2");
    if (title?.textContent !== heading) child.remove();
  }
}

function configureFooter(form, step, skip) {
  const footer = findDescendant(
    form,
    (item) => String(item.className || "").split(/\s+/).includes("editor-actions"),
  );
  if (!footer) return;
  const controls = Array.from(footer.children);
  const primary = controls.find((item) => item.tagName === "BUTTON" && item.type === "submit");
  const secondary = controls.find((item) => item.tagName === "BUTTON" && item !== primary);
  if (primary) {
    primary.textContent = step === "connection"
      ? "创建并继续"
      : step === "models"
        ? "保存并继续"
        : "保存并完成";
  }
  if (secondary) {
    secondary.textContent = step === "connection" ? "返回列表" : "上一步";
  }
  if (step === "models" || step === "routing") {
    const skipButton = actionButton(
      step === "models" ? "暂不录入" : "暂不启用智能路由",
      "button-secondary",
    );
    skipButton.addEventListener("click", skip);
    footer.append(skipButton);
  }
}

function stepNavigation(currentIndex) {
  const navigation = element("ol", "profile-create-steps");
  navigation.setAttribute("aria-label", "Profile 创建步骤");
  for (const [, label] of steps) {
    const index = navigation.children.length;
    const item = textElement(
      "li",
      label,
      `profile-create-step${index === currentIndex ? " is-current" : ""}${index < currentIndex ? " is-complete" : ""}`,
    );
    if (index === currentIndex) item.setAttribute("aria-current", "step");
    navigation.append(item);
  }
  return navigation;
}

function renderComplete(root, profile, complete) {
  const page = element("div", "profile-create-wizard stack");
  page.append(stepNavigation(3));
  const panel = element("section", "card setup-panel stack");
  panel.append(
    textElement("p", "Profile 已创建", "eyebrow"),
    textElement("h1", "配置完成"),
    textElement("code", `/${profile.slug}/v1/…`),
    textElement("p", "可以继续配置视觉增强、容错和 Agent，也可以稍后再处理。", "muted"),
  );
  const links = element("div", "cluster profile-create-complete-links");
  links.append(
    linkElement("打开 Profile 概况", `/_admin/profiles/${profile.id}/overview`),
    linkElement("视觉增强", `/_admin/profiles/${profile.id}/vision`),
    linkElement("容错", `/_admin/profiles/${profile.id}/reliability`),
    linkElement("Agent 配置", `/_admin/profiles/${profile.id}/agents`),
  );
  const finish = actionButton("完成");
  finish.addEventListener("click", () => complete(profile));
  panel.append(links, finish);
  page.append(panel);
  root.replaceChildren(page);
}

function findDescendant(root, predicate) {
  if (predicate(root)) return root;
  for (const child of root.children || []) {
    const found = findDescendant(child, predicate);
    if (found) return found;
  }
  return null;
}

function element(tagName, className = "") {
  const item = document.createElement(tagName);
  item.className = className;
  return item;
}

function textElement(tagName, text, className = "") {
  const item = element(tagName, className);
  item.textContent = text;
  return item;
}

function actionButton(text, variant = "") {
  const item = textElement("button", text, `button ${variant}`.trim());
  item.type = "button";
  return item;
}

function linkElement(text, href) {
  const item = textElement("a", text, "button button-secondary");
  item.setAttribute("href", href);
  return item;
}
