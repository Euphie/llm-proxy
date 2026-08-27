import { expect, test, type Page } from "@playwright/test";

async function expectHash(page: Page, value: string) {
  await expect.poll(async () => decodeURIComponent(new URL(page.url()).hash.slice(1))).toBe(value);
}

function relativeLuminance(color: string) {
  const channels = color.match(/[\d.]+/g)?.slice(0, 3).map(Number);
  if (!channels || channels.length !== 3) {
    throw new Error(`Unsupported RGB color: ${color}`);
  }
  const [red, green, blue] = channels.map((channel) => {
    const value = channel / 255;
    return value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * red + 0.7152 * green + 0.0722 * blue;
}

function contrastRatio(foreground: string, background: string) {
  const light = Math.max(relativeLuminance(foreground), relativeLuminance(background));
  const dark = Math.min(relativeLuminance(foreground), relativeLuminance(background));
  return (light + 0.05) / (dark + 0.05);
}

test("navigates the ten current chapters and pagination", async ({ page }) => {
  await page.goto("/docs/overview");

  await expect(page.getByRole("heading", { level: 1, name: "系统概览" })).toBeVisible();
  const chapterLinks = page.locator('.sidebar-navigation a[href^="/docs/"]');
  await expect(chapterLinks).toHaveCount(10);

  await page.getByRole("link", { name: /Profile 与模型目录/ }).click();
  await expect(page).toHaveURL(/\/docs\/profiles-models$/);
  await expect(page.getByRole("heading", { level: 1, name: "Profile 与模型目录" })).toBeFocused();

  const nextChapter = page.getByRole("link", { name: /下一章.*Routing Policy/ });
  await nextChapter.focus();
  await page.keyboard.press("Enter");
  await expect(page).toHaveURL(/\/docs\/routing-policy$/);
  await expect(page.getByRole("heading", { level: 1, name: "Routing Policy" })).toBeFocused();
});

test("handles valid and invalid heading hashes with browser history", async ({ page }) => {
  await page.goto("/docs/overview#%E4%B8%A4%E7%A7%8D%E8%AF%B7%E6%B1%82%E6%A8%A1%E5%BC%8F");
  await expectHash(page, "两种请求模式");
  await expect(page.getByRole("heading", { level: 2, name: "两种请求模式", exact: true })).toBeInViewport();

  await page.goto("/docs/overview#not-a-heading");
  await expectHash(page, "not-a-heading");
  await expect(page.getByRole("heading", { level: 1, name: "系统概览" })).toBeVisible();
  await expect(page.locator("#not-a-heading")).toHaveCount(0);

  await page.getByRole("link", { name: /下一章.*后台操作流程/ }).click();
  await expect(page).toHaveURL(/\/docs\/admin-workflow$/);
  await page.goBack();
  await expect(page).toHaveURL(/\/docs\/overview#not-a-heading$/);
  await page.goForward();
  await expect(page).toHaveURL(/\/docs\/admin-workflow$/);
});

test("searches chapter titles and section bodies", async ({ page }) => {
  await page.goto("/docs/overview");
  const search = page.getByRole("searchbox", { name: "搜索方案与模块" });

  await search.fill("Profile 与模型目录");
  await expect(page.getByRole("status")).toContainText("找到");
  await page.locator(".search-result").filter({ hasText: "Profile 与模型目录" }).first().click();
  await expect(page).toHaveURL(/\/docs\/profiles-models$/);

  await search.fill("URL 已选定的 Profile");
  const bodyResult = page.locator(".search-result").filter({ hasText: "Profile 是隔离边界" });
  await expect(bodyResult).toHaveCount(1);
  await bodyResult.click();
  await expect(page).toHaveURL(/\/docs\/profiles-models#profile-%E6%98%AF%E9%9A%94%E7%A6%BB%E8%BE%B9%E7%95%8C$/);
  await expect(page.getByRole("heading", { level: 2, name: "Profile 是隔离边界", exact: true })).toBeFocused();
});

test("loads one query-independent search index while the query changes", async ({ page }) => {
  let requests = 0;
  await page.route("**/api/search-index**", async (route) => {
    requests += 1;
    await new Promise((resolve) => setTimeout(resolve, 180));
    await route.continue();
  });
  await page.goto("/docs/overview");
  const search = page.getByRole("searchbox", { name: "搜索方案与模块" });

  await search.fill("Profile 配置");
  await search.fill("Profile 与模型目录");
  await expect(page.locator(".search-result").filter({ hasText: "Profile 与模型目录" }).first()).toBeVisible();
  expect(requests).toBe(1);
});

test("recovers a failed search index request without retaining stale errors", async ({ page }) => {
  let requests = 0;
  await page.route("**/api/search-index**", async (route) => {
    requests += 1;
    if (requests === 1) {
      await route.fulfill({ status: 503, body: "Unavailable" });
      return;
    }
    await route.continue();
  });
  await page.goto("/docs/overview");
  const search = page.getByRole("searchbox", { name: "搜索方案与模块" });

  await search.fill("Profile 与模型目录");
  await expect(page.getByRole("status")).toHaveText("搜索索引加载失败");
  await page.getByRole("button", { name: "重试" }).click();
  await expect(
    page.locator(".search-result").filter({ hasText: "Profile 与模型目录" }).first(),
  ).toBeVisible();
  await search.fill("URL 已选定的 Profile");
  await expect(page.locator(".search-result").filter({ hasText: "Profile 是隔离边界" })).toHaveCount(1);
  await search.fill("Profile 与模型目录");
  await expect(page.getByRole("status")).toContainText("找到");
  expect(requests).toBe(2);
});

test("asks stale document sessions to refresh when the search index version changes", async ({ page }) => {
  let requests = 0;
  await page.route("**/api/search-index**", async (route) => {
    requests += 1;
    await route.fulfill({
      status: 409,
      contentType: "application/json",
      body: JSON.stringify({
        error: "search_index_version_mismatch",
        currentVersion: "ffffffffffffffff",
      }),
    });
  });
  await page.goto("/docs/overview");
  await page.getByRole("searchbox", { name: "搜索方案与模块" }).fill("Profile");

  await expect(page.getByRole("status")).toHaveText("文档已更新，请刷新后搜索");
  await expect(page.getByRole("button", { name: "刷新文档" })).toBeVisible();
  await page.getByRole("searchbox", { name: "搜索方案与模块" }).fill("Upstream");
  expect(requests).toBe(1);
  await Promise.all([
    page.waitForEvent("domcontentloaded"),
    page.getByRole("button", { name: "刷新文档" }).click(),
  ]);
  await expect(page).toHaveURL(/\/docs\/overview$/);
});

test("focuses same-page section and page destinations on desktop and mobile", async ({ page }) => {
  for (const viewport of [
    { width: 1280, height: 800 },
    { width: 390, height: 844 },
  ]) {
    await page.setViewportSize(viewport);
    await page.goto("/docs/overview");
    if (viewport.width < 900) {
      await page.getByRole("button", { name: "打开章节导航" }).click();
    }
    const search = page.getByRole("searchbox", { name: "搜索方案与模块" });
    await search.fill("两种请求模式");
    await page.locator(".search-result").filter({ hasText: "两种请求模式" }).first().click();

    await expectHash(page, "两种请求模式");
    await expect(
      page.getByRole("heading", { level: 2, name: "两种请求模式", exact: true }),
    ).toBeFocused();
    if (viewport.width < 900) {
      await expect(page.locator("#docs-sidebar")).toHaveAttribute("aria-hidden", "true");
      await page.getByRole("button", { name: "打开章节导航" }).click();
    }

    await search.fill("系统概览");
    await page.locator(".search-result").filter({ hasText: "系统概览" }).first().click();
    await expect(page).toHaveURL(/\/docs\/overview$/);
    await expect(page.getByRole("heading", { level: 1, name: "系统概览" })).toBeFocused();

    if (viewport.width < 900) {
      await page.getByRole("button", { name: "打开章节导航" }).click();
    }
    await search.fill("");
    await page.getByRole("link", { name: /系统概览/ }).click();
    await expect(page.getByRole("heading", { level: 1, name: "系统概览" })).toBeFocused();
  }
});

test("persists the selected theme across navigation and reload", async ({ page }) => {
  const hydrationErrors: string[] = [];
  page.on("console", (message) => {
    if (message.type() === "error" && /hydration|did not match/i.test(message.text())) {
      hydrationErrors.push(message.text());
    }
  });
  await page.emulateMedia({ colorScheme: "light" });
  await page.goto("/docs/overview");
  await page.evaluate(() => window.localStorage.clear());
  await page.reload();

  const app = page.locator(".docs-app");
  await expect(app).toHaveAttribute("data-theme", "light");
  await page.getByRole("button", { name: "切换明暗主题" }).click();
  await expect(app).toHaveAttribute("data-theme", "dark");

  await page.getByRole("link", { name: /下一章.*后台操作流程/ }).click();
  await expect(page.locator(".docs-app")).toHaveAttribute("data-theme", "dark");
  await page.reload();
  await expect(page.locator(".docs-app")).toHaveAttribute("data-theme", "dark");
  expect(hydrationErrors).toEqual([]);
});

test("applies a saved dark theme before client hydration", async ({ page }) => {
  await page.addInitScript(() => {
    window.localStorage.setItem("llm-proxy-docs-theme", "dark");
  });
  await page.route(/\.js(?:\?|$)/, (route) => route.abort());
  await page.goto("/docs/profiles-models", { waitUntil: "domcontentloaded" });

  await expect(page.locator("html")).toHaveAttribute("data-docs-theme", "dark");
  await expect(page.locator(".docs-app")).toHaveCSS("color-scheme", "dark");
  const badge = page.locator(".status-badge--implemented").first();
  const colors = await badge.evaluate((element) => {
    const style = getComputedStyle(element);
    return { foreground: style.color, background: style.backgroundColor };
  });
  expect(contrastRatio(colors.foreground, colors.background)).toBeGreaterThanOrEqual(4.5);
});

test("tracks system and cross-tab theme changes", async ({ page, context }) => {
  await page.emulateMedia({ colorScheme: "light" });
  await page.goto("/docs/overview");
  await page.evaluate(() => window.localStorage.clear());
  await page.reload();
  await expect(page.locator(".docs-app")).toHaveAttribute("data-theme", "light");

  await page.emulateMedia({ colorScheme: "dark" });
  await expect(page.locator(".docs-app")).toHaveAttribute("data-theme", "dark");

  const peer = await context.newPage();
  await peer.goto("/docs/overview");
  await peer.evaluate(() => window.localStorage.setItem("llm-proxy-docs-theme", "light"));
  await expect(page.locator(".docs-app")).toHaveAttribute("data-theme", "light");
  await peer.close();
});

test("keeps theme switching functional when storage is unavailable", async ({ page }) => {
  const pageErrors: string[] = [];
  page.on("pageerror", (error) => pageErrors.push(error.message));
  await page.addInitScript(() => {
    Storage.prototype.setItem = () => {
      throw new DOMException("Storage unavailable", "SecurityError");
    };
  });
  await page.emulateMedia({ colorScheme: "light" });
  await page.goto("/docs/overview");
  await expect(page.locator(".docs-app")).toHaveAttribute("data-theme", "light");

  await page.getByRole("button", { name: "切换明暗主题" }).click();
  await expect(page.locator("html")).toHaveAttribute("data-docs-theme", "dark");
  await expect(page.locator(".docs-app")).toHaveAttribute("data-theme", "dark");
  expect(pageErrors).toEqual([]);
});

test("traps and restores focus in the mobile drawer and exposes the mobile TOC", async ({ page }) => {
  let searchRequests = 0;
  await page.route("**/api/search-index**", async (route) => {
    searchRequests += 1;
    await route.continue();
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/docs/overview");

  await expect(page.getByRole("button", { name: "打开章节导航" })).toBeVisible();
  const menu = page.locator(".mobile-menu");
  await menu.click();
  await expect(menu).toHaveAttribute("aria-expanded", "true");
  await expect(page.getByRole("button", { name: "打开章节导航" })).toHaveCount(0);
  const drawer = page.locator("#docs-sidebar");
  await expect(drawer).toHaveAttribute("role", "dialog");
  await expect(drawer).toHaveAttribute("aria-modal", "true");
  await expect(drawer).not.toHaveAttribute("aria-hidden", "true");
  await expect(page.locator(".mac-titlebar")).toHaveAttribute("inert", "");
  await expect(page.locator(".docs-scroll-region")).toHaveAttribute("inert", "");
  await expect(page.locator(".docs-scroll-region")).toHaveAttribute("aria-hidden", "true");
  await expect(page.getByRole("searchbox", { name: "搜索方案与模块" })).toBeFocused();
  await page.getByRole("link", { name: /下一章/, includeHidden: true }).first().focus();
  await expect(page.getByRole("searchbox", { name: "搜索方案与模块" })).toBeFocused();

  await drawer.getByRole("link").last().focus();
  await page.keyboard.press("Tab");
  await expect(drawer.getByRole("button", { name: "关闭章节导航" })).toBeFocused();

  await page.keyboard.press("Escape");
  await expect(menu).toHaveAttribute("aria-expanded", "false");
  await expect(page.locator("#docs-sidebar")).toHaveAttribute("aria-hidden", "true");
  await expect(page.locator(".mac-titlebar")).not.toHaveAttribute("inert", "");
  await expect(page.locator(".docs-scroll-region")).not.toHaveAttribute("inert", "");
  await expect(menu).toBeFocused();

  const tocButton = page.locator(".mobile-toc").getByRole("button", { name: "本页目录" });
  await expect(tocButton).toBeVisible();
  await tocButton.click();
  await expect(tocButton).toHaveAttribute("aria-expanded", "true");
  const boundaryLink = page.locator(".mobile-toc").getByRole("link", { name: "两种请求模式" });
  await expect(boundaryLink).toBeVisible();
  await boundaryLink.click();
  await expectHash(page, "两种请求模式");
  expect(searchRequests).toBe(0);
});

test("keeps the documentation usable with reduced motion", async ({ page }) => {
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.goto("/docs/overview");

  await expect(page.getByRole("heading", { level: 1, name: "系统概览" })).toBeVisible();
  const layout = await page.evaluate(() => {
    const windowFrame = document.querySelector(".mac-window");
    const animatedControl = document.querySelector(".icon-button");
    const bounds = windowFrame?.getBoundingClientRect();
    const duration = animatedControl ? getComputedStyle(animatedControl).transitionDuration : "";
    return {
      reduced: window.matchMedia("(prefers-reduced-motion: reduce)").matches,
      noHorizontalOverflow: document.documentElement.scrollWidth <= window.innerWidth,
      frameWithinViewport: Boolean(bounds && bounds.left >= 0 && bounds.right <= window.innerWidth),
      duration,
    };
  });
  expect(layout).toMatchObject({
    reduced: true,
    noHorizontalOverflow: true,
    frameWithinViewport: true,
  });
  expect(Number.parseFloat(layout.duration)).toBeLessThanOrEqual(0.001);
});

test("makes horizontally scrollable code keyboard accessible on mobile", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/docs/online-routing");

  const code = page.getByRole("group", { name: "text 代码块" }).first();
  await expect(code).toHaveAttribute("tabindex", "0");
  await code.focus();
  await expect(code).toBeFocused();
});

test("opens a routing diagram and restores trigger focus after Escape", async ({ page }) => {
  await page.goto("/docs/online-routing");

  const trigger = page.getByRole("button", { name: "放大查看：model=auto 在线请求链路" });
  await trigger.focus();
  await trigger.click();
  const dialog = page.getByRole("dialog", { name: "model=auto 在线请求链路" });
  await expect(dialog).toBeVisible();
  await expect(dialog.getByRole("button", { name: "关闭" })).toBeFocused();
  await expect(page.locator(".docs-app")).toHaveAttribute("inert", "");

  await page.keyboard.press("Escape");
  await expect(dialog).toHaveCount(0);
  await expect(trigger).toBeFocused();
  await expect(page.locator(".docs-app")).not.toHaveAttribute("inert", "");
});

test("keeps the final content reachable in a short desktop viewport", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 500 });
  await page.goto("/docs/operations");

  const scrollRegion = page.locator(".docs-scroll-region");
  const pagination = page.locator(".chapter-pagination");
  await scrollRegion.evaluate((element) => {
    element.style.scrollBehavior = "auto";
  });
  await expect.poll(async () => {
    await pagination.scrollIntoViewIfNeeded();
    return page.evaluate(() => {
      const frame = document.querySelector(".mac-window")?.getBoundingClientRect();
      const region = document.querySelector(".docs-scroll-region");
      const paginationBox = document.querySelector(".chapter-pagination")?.getBoundingClientRect();
      return {
        frameWithinViewport: Boolean(frame && frame.top >= 0 && frame.bottom <= window.innerHeight),
        paginationInViewport: Boolean(
          paginationBox && paginationBox.bottom > 0 && paginationBox.top < window.innerHeight,
        ),
        reachedBottom: Boolean(region && region.scrollTop + region.clientHeight >= region.scrollHeight - 1),
      };
    });
  }).toEqual({ frameWithinViewport: true, paginationInViewport: true, reachedBottom: true });
});
