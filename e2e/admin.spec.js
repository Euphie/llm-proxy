import { expect, test } from "@playwright/test";
import http from "node:http";

const password = "secure-admin-2026";
const upstreamPort = 4055;
const upstreamRequests = [];

const upstream = http.createServer((request, response) => {
  const chunks = [];
  request.on("data", (chunk) => chunks.push(chunk));
  request.on("end", () => {
    const body = JSON.parse(Buffer.concat(chunks).toString("utf8"));
    upstreamRequests.push({ path: request.url, body });
    response.setHeader("content-type", "application/json");
    if (body.stream === false) {
      response.end(
        JSON.stringify({
          output: [
            {
              type: "message",
              content: [{ type: "output_text", text: "fixture image description" }],
            },
          ],
        }),
      );
      return;
    }
    response.end(JSON.stringify({ output: [] }));
  });
});

test.beforeAll(async ({ request }) => {
  await new Promise((resolve, reject) => {
    upstream.once("error", reject);
    upstream.listen(upstreamPort, "0.0.0.0", resolve);
  });
  await expect.poll(async () => {
    try {
      return (await request.get("/_admin/")).status();
    } catch {
      return 0;
    }
  }).toBe(200);
});

test.afterAll(async () => {
  await new Promise((resolve, reject) => {
    upstream.close((error) => (error ? reject(error) : resolve()));
  });
});

test("persists model capabilities and applies them to Agent and vision workflows", async ({
  page,
  request,
}) => {
  const baseURL = process.env.BASE_URL;

  await page.goto("/_admin/");
  await expect(page.locator(".auth-window")).toBeVisible();
  await expect(page.getByText("首次登录存在公网抢占风险")).toBeVisible();

  await page.getByLabel("用户名").fill("admin");
  await page.getByLabel("密码").fill("admin");
  await page.getByRole("button", { name: "登录" }).click();

  await expect(
    page.getByRole("heading", { name: "修改初始密码" }),
  ).toBeVisible();
  await page.getByLabel("当前密码").fill("admin");
  await page.getByLabel("新密码", { exact: true }).fill(password);
  await page.getByLabel("确认新密码").fill(password);
  await page.getByRole("button", { name: "保存新密码" }).click();

  await page.getByRole("button", { name: "创建第一个 Profile" }).click();
  await page.getByLabel("名称").fill("Coding");
  await page.getByLabel("Slug").fill("coding");
  await page.getByLabel("协议").selectOption("anthropic");
  await page.getByLabel("Upstream").fill(`http://e2e:${upstreamPort}`);
  const defaultProfile = page.getByLabel("设为默认 Profile");
  await expect(defaultProfile).toBeChecked();
  await expect(defaultProfile).toBeDisabled();
  await page.getByRole("button", { name: "保存 Profile" }).click();

  await expect(page.getByRole("heading", { name: "Coding" })).toBeVisible();
  await expect(page.getByText("coding", { exact: true })).toBeVisible();
  await expect(page.getByText("默认", { exact: true })).toBeVisible();
  const appWindow = page.locator(".app-window");
  await expect(appWindow).toBeVisible();
  await expect(page.locator(".window-control")).toHaveCount(3);
  await expect(page.locator(".window-controls")).toHaveAttribute(
    "aria-hidden",
    "true",
  );
  await expect(page.locator(".app-sidebar")).toBeVisible();
  await expect(page.locator(".profile-settings-row")).toHaveCount(1);

  const desktopMaterial = await appWindow.evaluate((element) => {
    const body = element.querySelector(".app-window-body");
    const sidebar = element.querySelector(".app-sidebar");
    const content = element.querySelector(".app-content");
    const style = getComputedStyle(element);
    return {
      background: style.backgroundColor,
      radius: style.borderRadius,
      bodyDisplay: getComputedStyle(body).display,
      sidebarRight: sidebar.getBoundingClientRect().right,
      contentLeft: content.getBoundingClientRect().left,
    };
  });
  expect(desktopMaterial.background).not.toBe("rgba(0, 0, 0, 0)");
  expect(parseFloat(desktopMaterial.radius)).toBeGreaterThan(0);
  expect(desktopMaterial.bodyDisplay).toBe("grid");
  expect(desktopMaterial.contentLeft).toBeGreaterThanOrEqual(
    desktopMaterial.sidebarRight - 1,
  );

  await page.getByRole("button", { name: "编辑" }).click();
  const addModel = page.getByRole("button", { name: "添加模型" });
  await expect(addModel.locator("..")).toHaveClass(/model-list-actions/);
  const addModelAlignment = await addModel.evaluate((button) => ({
    buttonRight: button.getBoundingClientRect().right,
    actionsRight: button.parentElement.getBoundingClientRect().right,
  }));
  expect(
    Math.abs(addModelAlignment.actionsRight - addModelAlignment.buttonRight),
  ).toBeLessThan(2);
  await addModel.click();
  const firstModelID = page.locator('[name="model-0-id"]');
  const firstModelContext = page.locator(
    '[name="model-0-context-window"]',
  );
  const firstModelOutput = page.locator(
    '[name="model-0-max-output-tokens"]',
  );
  const firstModelVision = page.locator(
    '[name="model-0-supports-vision"]',
  );
  await firstModelID.evaluate((input) => {
    const modelID = "claude-haiku-4-5-20251001";
    input.value = modelID;
    input.dispatchEvent(new InputEvent("input", {
      bubbles: true,
      data: modelID,
      inputType: "insertFromPaste",
    }));
  });
  await expect(firstModelID).toHaveValue("claude-haiku-4-5-20251001");
  await expect(firstModelContext).toHaveValue("200000");
  await expect(firstModelOutput).toHaveValue("64000");
  await expect(firstModelVision).toHaveValue("true");

  await firstModelID.fill("claude-glm-5.2");
  await firstModelID.blur();
  await expect(firstModelID).toHaveValue("claude-glm-5.2");
  await expect(firstModelContext).toHaveValue("1000000");
  await expect(firstModelOutput).toHaveValue("131072");
  await expect(firstModelVision).toHaveValue("false");

  await firstModelID.fill("GLM-5");
  await firstModelID.blur();
  await expect(firstModelContext).toHaveValue("204800");
  await firstModelOutput.fill("");
  await page.getByRole("button", { name: "添加模型" }).click();
  const secondModelID = page.locator('[name="model-1-id"]');
  await secondModelID.fill("Kimi-K2.5");
  await secondModelID.blur();
  await expect(page.locator('[name="model-1-context-window"]')).toHaveValue(
    "262144",
  );
  const secondModelOutput = page.locator(
    '[name="model-1-max-output-tokens"]',
  );
  await expect(secondModelOutput).toHaveValue("");
  await secondModelOutput.fill("65536");
  await expect(page.locator('[name="model-1-supports-vision"]')).toHaveValue(
    "true",
  );
  await page.getByRole("button", { name: "添加模型" }).click();
  const previewRow = page.locator(".model-capability-row").nth(2);
  await previewRow.getByLabel("模型 ID").fill("gpt-5.6-sol-preview");
  await expect(previewRow.getByLabel("上下文窗口")).toHaveValue("");
  await expect(previewRow.locator(".model-recommendation-card")).toContainText(
    "同系列候选",
  );
  await previewRow
    .getByRole("button", { name: "应用 GPT-5.6 Sol 推荐值" })
    .click();
  await expect(previewRow.getByLabel("模型 ID")).toHaveValue(
    "gpt-5.6-sol-preview",
  );
  await expect(previewRow.getByLabel("上下文窗口")).toHaveValue("1050000");
  await expect(previewRow.getByLabel("最大输出 Token")).toHaveValue("128000");
  await expect(previewRow.getByLabel("支持视觉")).toHaveValue("true");
  await previewRow.getByRole("button", { name: "删除模型" }).click();
  await page.getByLabel("启用视觉预处理").check();
  await page.getByText("视觉参数", { exact: true }).click();
  await page.getByLabel("识图模型").fill("Kimi-K2.5");
  await page.getByLabel("未收录模型").selectOption("bypass");
  await page.getByRole("button", { name: "保存 Profile" }).click();

  await page.reload();
  await page.getByRole("button", { name: "编辑" }).click();
  await expect(page.locator('[name="model-0-id"]')).toHaveValue("GLM-5");
  await expect(page.locator('[name="model-0-context-window"]')).toHaveValue("204800");
  await expect(page.locator('[name="model-0-max-output-tokens"]')).toHaveValue("");
  await expect(page.locator('[name="model-0-supports-vision"]')).toHaveValue("false");
  await expect(page.locator('[name="model-1-id"]')).toHaveValue("Kimi-K2.5");
  await expect(page.locator('[name="model-1-context-window"]')).toHaveValue("262144");
  await expect(page.locator('[name="model-1-max-output-tokens"]')).toHaveValue("65536");
  await expect(page.locator('[name="model-1-supports-vision"]')).toHaveValue("true");
  await expect(page.getByLabel("启用视觉预处理")).toBeChecked();
  await page.getByText("视觉参数", { exact: true }).click();
  await expect(page.getByLabel("识图模型")).toHaveValue("Kimi-K2.5");
  await expect(page.getByLabel("未收录模型")).toHaveValue("bypass");
  await page.getByRole("button", { name: "返回列表" }).click();

  await page.getByRole("button", { name: "生成配置" }).click();
  const generator = page.getByRole("dialog");
  await expect(generator).toBeVisible();
  await expect(generator.getByText(`${baseURL}/coding`, { exact: false })).toBeVisible();
  await generator.getByLabel("Sonnet 模型").fill("Kimi-K2.5");
  await generator
    .getByLabel("认证变量")
    .selectOption("ANTHROPIC_AUTH_TOKEN");
  await expect(
    generator.getByText('"ANTHROPIC_DEFAULT_SONNET_MODEL": "Kimi-K2.5"', {
      exact: false,
    }),
  ).toBeVisible();
  await expect(
    generator.getByText('"CLAUDE_CODE_AUTO_COMPACT_WINDOW": "262144"', {
      exact: false,
    }),
  ).toBeVisible();
  await expect(
    generator.getByText('"CLAUDE_AUTOCOMPACT_PCT_OVERRIDE": "75"', {
      exact: false,
    }),
  ).toBeVisible();
  await expect(
    generator.getByText('"ANTHROPIC_AUTH_TOKEN": "<SET_LOCALLY>"', {
      exact: false,
    }),
  ).toBeVisible();
  await expect(generator.getByLabel(/密钥|secret|key/i)).toHaveCount(0);
  await expect(generator).not.toContainText(password);

  await generator.getByRole("button", { name: "返回 Profiles" }).click();
  await page.getByRole("button", { name: "编辑" }).click();
  await page.getByLabel("协议").selectOption("openai");
  await page.getByRole("button", { name: "保存 Profile" }).click();
  await page.getByRole("button", { name: "生成配置" }).click();
  const openAIGenerator = page.getByRole("dialog");
  await openAIGenerator.getByLabel("Agent").selectOption("opencode-responses");
  const openCodeModel = openAIGenerator.getByLabel("模型 ID");
  const openCodeOutput = openAIGenerator.locator(".generator-output");
  const openCodeWarning = openAIGenerator.locator(
    ".generator-capability-warning",
  );
  await openCodeModel.fill("GLM-5");
  await expect(openCodeOutput).toContainText(
    '"model": "llm-proxy-coding/GLM-5"',
  );
  await expect(openCodeOutput).not.toContainText('"limit"');
  await expect(openCodeOutput).not.toContainText('"compaction"');
  await expect(openCodeWarning).toBeVisible();
  await expect(openCodeWarning).toContainText(/OpenCode.*输出|输出.*OpenCode/);

  await openCodeModel.fill("Kimi-K2.5");
  await expect(
    openAIGenerator.getByText('"model": "llm-proxy-coding/Kimi-K2.5"', {
      exact: false,
    }),
  ).toBeVisible();
  await expect(
    openAIGenerator.getByText('"context": 262144', { exact: false }),
  ).toBeVisible();
  await expect(
    openAIGenerator.getByText('"output": 65536', { exact: false }),
  ).toBeVisible();
  await expect(
    openAIGenerator.getByText('"reserved": 65536', { exact: false }),
  ).toBeVisible();
  await expect(openCodeWarning).toBeHidden();
  await expect(openAIGenerator).not.toContainText(password);

  await openAIGenerator.getByLabel("Agent").selectOption("codex-responses");
  await openAIGenerator.getByLabel("模型 ID").fill("Kimi-K2.5");
  await expect(
    openAIGenerator.getByText("model_context_window = 262144", {
      exact: false,
    }),
  ).toBeVisible();
  await expect(
    openAIGenerator.getByText("model_auto_compact_token_limit = 196608", {
      exact: false,
    }),
  ).toBeVisible();
  await expect(
    openAIGenerator.getByText('wire_api = "responses"', { exact: false }),
  ).toBeVisible();
  await expect(openAIGenerator).not.toContainText(password);
  await openAIGenerator.getByRole("button", { name: "返回 Profiles" }).click();

  upstreamRequests.length = 0;
  const nativeVision = await request.post("/coding/v1/responses", {
    headers: { "content-type": "application/json" },
    data: responsesImageRequest("Kimi-K2.5"),
  });
  expect(nativeVision.status()).toBe(200);
  expect(upstreamRequests).toHaveLength(1);
  expect(upstreamRequests[0].body.model).toBe("Kimi-K2.5");
  expect(upstreamRequests[0].body.stream).not.toBe(false);

  upstreamRequests.length = 0;
  const textOnly = await request.post("/coding/v1/responses", {
    headers: { "content-type": "application/json" },
    data: responsesImageRequest("GLM-5"),
  });
  expect(textOnly.status()).toBe(200);
  expect(upstreamRequests).toHaveLength(2);
  expect(
    upstreamRequests.filter(
      (entry) => entry.body.model === "Kimi-K2.5" && entry.body.stream === false,
    ),
  ).toHaveLength(1);
  expect(
    upstreamRequests.filter(
      (entry) => entry.body.model === "GLM-5" && entry.body.stream !== false,
    ),
  ).toHaveLength(1);

  await page.setViewportSize({ width: 390, height: 844 });
  const mobileLayout = await page
    .locator(".app-window-body")
    .evaluate((element) => {
      const sidebar = element.querySelector(".app-sidebar");
      const content = element.querySelector(".app-content");
      const navigation = element.querySelector(".app-nav");
      return {
        sidebarBottom: sidebar.getBoundingClientRect().bottom,
        contentTop: content.getBoundingClientRect().top,
        navigationDisplay: getComputedStyle(navigation).display,
      };
    });
  expect(mobileLayout.contentTop).toBeGreaterThanOrEqual(
    mobileLayout.sidebarBottom - 1,
  );
  expect(mobileLayout.navigationDisplay).toBe("flex");
  await expect(
    page.getByRole("navigation", { name: "主导航" }),
  ).toBeVisible();

  await page.getByRole("button", { name: "退出" }).click();
  await page.goto("/_admin/profiles");
  await expect(page.getByRole("heading", { name: "登录" })).toBeVisible();
});

function responsesImageRequest(model) {
  return {
    model,
    input: [
      {
        role: "user",
        content: [
          {
            type: "input_image",
            image_url: "data:image/png;base64,aGVsbG8=",
            detail: "low",
          },
        ],
      },
    ],
  };
}
