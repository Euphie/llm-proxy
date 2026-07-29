import { expect, test } from "@playwright/test";

const password = "secure-admin-2026";

test.beforeAll(async ({ request }) => {
  await expect.poll(async () => {
    try {
      return (await request.get("/_admin/")).status();
    } catch {
      return 0;
    }
  }).toBe(200);
});

test("initializes admin, creates a Profile, and generates temporary configuration", async ({
  page,
}) => {
  const baseURL = process.env.BASE_URL;

  await page.goto("/_admin/");
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
  await page.getByLabel("Upstream").fill("https://example.test/anthropic");
  const defaultProfile = page.getByLabel("设为默认 Profile");
  await expect(defaultProfile).toBeChecked();
  await expect(defaultProfile).toBeDisabled();
  await page.getByRole("button", { name: "保存 Profile" }).click();

  await expect(page.getByRole("heading", { name: "Coding" })).toBeVisible();
  await expect(page.getByText("coding", { exact: true })).toBeVisible();
  await expect(page.getByText("默认", { exact: true })).toBeVisible();

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
    generator.getByText('"ANTHROPIC_AUTH_TOKEN": "<SET_LOCALLY>"', {
      exact: false,
    }),
  ).toBeVisible();
  await expect(generator.getByLabel(/密钥|secret|key/i)).toHaveCount(0);

  await generator.getByRole("button", { name: "返回 Profiles" }).click();
  await page.reload();
  await page.getByRole("button", { name: "生成配置" }).click();
  const reopenedGenerator = page.getByRole("dialog");
  await expect(reopenedGenerator.getByLabel("Sonnet 模型")).toHaveValue("");
  await reopenedGenerator.getByRole("button", { name: "返回 Profiles" }).click();

  await page.getByRole("button", { name: "退出" }).click();
  await page.goto("/_admin/profiles");
  await expect(page.getByRole("heading", { name: "登录" })).toBeVisible();
});
