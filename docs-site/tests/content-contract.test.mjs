import assert from "node:assert/strict";
import { access, readdir, readFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import { findAffirmativeCrossProfileClaims, isEvidenceLevel } from "../lib/content-contract.ts";

const siteRoot = fileURLToPath(new URL("../", import.meta.url));
const canonicalChapters = [
  ["overview", "系统概览", "说明显式模型与 model=auto 的边界，以及当前系统实际负责什么。", "理解方案", "compass", "blue"],
  ["admin-workflow", "后台操作流程", "按照当前管理后台页面完成 Profile、模型、策略和 Agent 配置。", "理解方案", "sliders", "indigo"],
  ["profiles-models", "Profile 与模型目录", "说明连接配置、模型事实、状态和运行时修订如何立即生效。", "理解方案", "layers", "cyan"],
  ["routing-policy", "Routing Policy", "说明策略生成、编辑、生产资格、立即生效和历史回滚。", "控制面", "git-branch", "indigo"],
  ["online-routing", "在线请求链路", "从任务分析、候选门槛到执行计划解释一次 model=auto 请求。", "运行时", "route", "green"],
  ["vision", "视觉预处理", "解释原生视觉与复合视觉、识图缓存以及失败边界。", "运行时", "activity", "teal"],
  ["reliability", "重试、超时与 Session", "解释 overload_rules、统一预算、ClientCommit 和 Session 锁定。", "运行时", "shield", "slate"],
  ["evaluation", "评测与自动校准", "说明公开评测、本地证据、Shadow 和自动生产资格更新。", "控制面", "activity", "orange"],
  ["observability", "统计与故障排查", "使用路由轨迹、物理调用和模型表现定位请求问题。", "交付", "database", "yellow"],
  ["operations", "运行边界与上线检查", "汇总 Profile 隔离、网关职责、热更新和上线验收边界。", "交付", "shield", "red"],
].map(([slug, title, summary, group, icon, accent]) => ({
  slug,
  title,
  summary,
  group,
  icon,
  accent,
  implementation_status: "已实现",
  evidence_level: "本项目源码与测试验证",
}));

const canonicalDiagrams = [
  "system-architecture",
  "online-routing-flow",
  "vision-processing-flow",
  "retry-timeout-state-machine",
  "session-lock-lifecycle",
  "policy-calibration-loop",
  "observability-troubleshooting",
];

async function readJson(relativePath) {
  return JSON.parse(await readFile(path.join(siteRoot, relativePath), "utf8"));
}

test("locks the ten current chapter records", async () => {
  const manifest = await readJson("data/chapters.json");
  assert.deepEqual(manifest.chapters, canonicalChapters);
  for (const chapter of manifest.chapters) {
    assert.equal(isEvidenceLevel(chapter.evidence_level), true);
  }
});

test("keeps one Markdown source per current chapter and removes old site chapters", async () => {
  const filenames = (await readdir(path.join(siteRoot, "content")))
    .filter((name) => name.endsWith(".md"))
    .sort();
  assert.deepEqual(
    filenames,
    canonicalChapters.map(({ slug }) => `${slug}.md`).sort(),
  );

  for (const chapter of canonicalChapters) {
    const content = await readFile(path.join(siteRoot, "content", `${chapter.slug}.md`), "utf8");
    assert.match(content, new RegExp(`^# ${chapter.title.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`, "m"));
    assert.doesNotMatch(content, /\[!(?:TARGET|FUTURE)\]/);
  }
});

test("documents the current routing controls and failure boundaries", async () => {
  const contents = await Promise.all(canonicalChapters.map(({ slug }) => (
    readFile(path.join(siteRoot, "content", `${slug}.md`), "utf8")
  )));
  const joined = contents.join("\n");

  for (const term of [
    "model=auto",
    "Production",
    "Shadow",
    "session_lock_token_threshold",
    "100K",
    "overload_rules",
    "ClientCommit",
    "auto_update_policy",
    "0 / network",
    "模型目录修订",
  ]) {
    assert.match(joined, new RegExp(term), `Current documentation is missing ${term}.`);
  }
});

test("ships one valid Excalidraw source and SVG export for every documented flow", async () => {
  const markdown = await Promise.all(canonicalChapters.map(({ slug }) => (
    readFile(path.join(siteRoot, "content", `${slug}.md`), "utf8")
  )));
  const referenced = new Set(markdown.flatMap((content) => (
    [...content.matchAll(/\/diagrams\/([a-z0-9-]+)\.svg/g)].map(([, name]) => name)
  )));

  for (const name of canonicalDiagrams) {
    const source = await readJson(`diagrams/${name}.excalidraw`);
    const svg = await readFile(path.join(siteRoot, "public/diagrams", `${name}.svg`), "utf8");
    assert.equal(source.type, "excalidraw", `${name} has the wrong document type.`);
    assert.equal(source.version, 2, `${name} has the wrong document version.`);
    assert.equal(source.appState?.viewBackgroundColor, "#ffffff");
    assert.deepEqual(source.files, {});
    assert.ok(source.elements.length >= 10, `${name} must remain a substantive diagram.`);
    assert.match(svg, /^<svg\b/);
    assert.equal(referenced.has(name), true, `${name} is not referenced by a chapter.`);

    const ids = source.elements.map(({ id }) => id);
    const seeds = source.elements.map(({ seed }) => seed);
    assert.equal(new Set(ids).size, ids.length, `${name} contains duplicate element IDs.`);
    assert.equal(new Set(seeds).size, seeds.length, `${name} contains duplicate seeds.`);
    const byId = new Map(source.elements.map((element) => [element.id, element]));
    const arrows = source.elements.filter(({ type }) => type === "arrow");
    assert.ok(arrows.length <= 12, `${name} must keep its arrow count at or below 12.`);

    for (const element of source.elements) {
      for (const field of [
        "id", "type", "x", "y", "width", "height", "angle", "strokeColor",
        "backgroundColor", "fillStyle", "strokeWidth", "strokeStyle", "roughness",
        "opacity", "groupIds", "roundness", "seed", "version", "isDeleted",
        "boundElements", "updated", "link", "locked",
      ]) {
        assert.ok(Object.hasOwn(element, field), `${name}:${element.id} is missing ${field}.`);
      }
      for (const forbidden of ["frameId", "index", "versionNonce", "rawText"]) {
        assert.equal(Object.hasOwn(element, forbidden), false, `${name}:${element.id} must omit ${forbidden}.`);
      }
      assert.equal(element.roughness, 0, `${name}:${element.id} must use clean strokes.`);
      assert.notDeepEqual(element.boundElements, [], `${name}:${element.id} must use null instead of an empty binding list.`);

      if (element.type === "text") {
        assert.match(element.strokeColor, /^#[0-9a-f]{6}$/i, `${name}:${element.id} needs an explicit text color.`);
        assert.equal(element.fontFamily, 2);
        if (element.containerId) {
          const container = byId.get(element.containerId);
          assert.ok(container, `${name}:${element.id} references a missing container.`);
          assert.ok(
            container.boundElements?.some((binding) => binding.type === "text" && binding.id === element.id),
            `${name}:${element.id} is not reciprocally bound to its container.`,
          );
        }
      }

      if (element.type === "arrow") {
        const maximumSpan = Math.max(...element.points.flatMap(([x, y]) => [Math.abs(x), Math.abs(y)]));
        assert.ok(maximumSpan <= 400, `${name}:${element.id} spans too much of the diagram.`);
        assert.deepEqual(element.points[0], [0, 0], `${name}:${element.id} must start at the arrow origin.`);
        for (let index = 1; index < element.points.length; index += 1) {
          const [previousX, previousY] = element.points[index - 1];
          const [currentX, currentY] = element.points[index];
          assert.ok(
            previousX === currentX || previousY === currentY,
            `${name}:${element.id} contains a diagonal segment at point ${index}.`,
          );
        }
        for (const endpoint of ["startBinding", "endBinding"]) {
          const binding = element[endpoint];
          assert.ok(binding?.elementId, `${name}:${element.id} is missing ${endpoint}.`);
          const target = byId.get(binding.elementId);
          assert.ok(target, `${name}:${element.id} references a missing arrow endpoint.`);
          assert.ok(
            target.boundElements?.some((candidate) => candidate.type === "arrow" && candidate.id === element.id),
            `${name}:${element.id} is not reciprocally bound to ${binding.elementId}.`,
          );
        }
      }
    }
  }
});

test("forbids affirmative cross-Profile execution claims", async () => {
  for (const { slug } of canonicalChapters) {
    const content = await readFile(path.join(siteRoot, "content", `${slug}.md`), "utf8");
    assert.deepEqual(
      findAffirmativeCrossProfileClaims(content),
      [],
      `${slug} must keep routing and evaluation inside one Profile`,
    );
  }

  assert.deepEqual(findAffirmativeCrossProfileClaims("禁止跨 Profile 路由"), []);
  assert.deepEqual(findAffirmativeCrossProfileClaims("允许跨 Profile 路由"), ["允许跨 Profile 路由"]);
});

test("preserves the Mesotes brand assets used by the documentation shell", async () => {
  await Promise.all([
    access(path.join(siteRoot, "public/brand/mesotes-mark.png")),
    access(path.join(siteRoot, "public/brand/mesotes-mark-256.png")),
    access(path.join(siteRoot, "public/favicon.svg")),
  ]);
});
