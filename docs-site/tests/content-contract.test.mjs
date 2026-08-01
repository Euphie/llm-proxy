import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { readdir, readFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

import { findAffirmativeCrossProfileClaims } from "../lib/content-contract.ts";

const siteRoot = fileURLToPath(new URL("../", import.meta.url));
const expectedSlugs = [
  "overview", "source-baseline", "competitor-evidence", "core-model",
  "profile-isolation", "online-routing", "quality-and-cost",
  "target-reliability", "runtime-reliability", "evaluation-feedback",
  "strategy-lifecycle", "engineering-and-delivery",
];
const canonicalChapters = [
  ["overview", "产品目标与非目标", "以单一 Profile 边界组织模型选择、Target 调度和受控学习。", "理解方案", "compass", "blue", "本版目标", "本项目源码验证"],
  ["source-baseline", "源码基线与现状差距", "用固定提交区分当前运行时事实、缺口与本版目标。", "理解方案", "layers", "cyan", "已实现", "本项目源码验证"],
  ["competitor-evidence", "竞品源码证据矩阵", "从固定源码提炼可采用机制，并明确拒绝跨 Profile 编排。", "理解方案", "activity", "orange", "本版目标", "竞品源码验证"],
  ["core-model", "核心对象与配置模型", "定义全部归属单一 Profile 的路由、策略、计划与证据对象。", "理解方案", "sliders", "indigo", "本版目标", "竞品源码验证"],
  ["profile-isolation", "Profile 隔离与协议边界", "把 Profile 固化为 Provider 实例、信任域和唯一运行时隔离边界。", "运行时", "shield", "red", "本版目标", "本项目源码验证"],
  ["online-routing", "在线决策流水线", "通过本地事实、硬约束、模型选择和 Target 调度生成不可变计划。", "运行时", "route", "green", "本版目标", "竞品源码验证"],
  ["quality-and-cost", "质量目标与模型选择", "先通过 Route 级质量门禁，再按条件优化成本与延迟。", "运行时", "scale", "yellow", "本版目标", "竞品源码验证"],
  ["target-reliability", "Target 调度、容量与健康", "在同一 Profile、同一逻辑模型内完成准入、健康过滤和负载调度。", "运行时", "activity", "teal", "本版目标", "竞品源码验证"],
  ["runtime-reliability", "尝试预算、流式与会话", "用统一 AttemptBudget 和 ClientCommit 建立有界可靠性。", "运行时", "shield", "slate", "本版目标", "竞品源码验证"],
  ["evaluation-feedback", "评测、反馈与稳定实验", "用 Profile-scoped 评测、强类型反馈和稳定分桶积累证据。", "控制面", "activity", "orange", "本版目标", "竞品源码验证"],
  ["strategy-lifecycle", "策略生命周期、观测与重放", "以不可变版本、CAS、灰度、LKG 和授权重放治理发布。", "控制面", "git-branch", "indigo", "本版目标", "竞品源码验证"],
  ["engineering-and-delivery", "API、迁移与交付验收", "明确破坏性迁移、五个子项目和双重验收门。", "交付", "database", "slate", "本版目标", "本项目源码验证"],
].map(([slug, title, summary, group, icon, accent, implementation_status, evidence_level]) => ({
  slug, title, summary, group, icon, accent, implementation_status, evidence_level,
}));
const competitorCommits = {
  LiteLLM: "de706a35a6f1e9cb8c3cb527271df0b76a69f410",
  Portkey: "669825cbe89ee51569918b8f78a9db486fd69dd4",
  Helicone: "9649b27bdc9fb0907d359e899894102a15f3a085",
  "vLLM Semantic Router": "332f2d38deba21bbb5f31b875fb3266f09c33b58",
  TensorZero: "62eb8f63e8ec62018d70420dbf1a8c5d1c026315",
  RouteLLM: "0b64fdafe049e596a3f5657c219329f24af24198",
};
const canonicalDiagrams = [
  ["attempt-budget", "runtime-reliability"],
  ["profile-isolation", "profile-isolation"],
  ["routing-pipeline", "online-routing"],
  ["strategy-lifecycle", "strategy-lifecycle"],
  ["target-reliability", "target-reliability"],
];
const forbiddenDiagramDesign = /provider_kind|provider_id|\bProviders?\b|cross-Profile|跨\s*Profile|grade[_ -]?[a-f]|policy[_ -]?20\d{2}|\b\d+(?:\.\d+)?%|\b(?:max[_ -]?)?(?:attempts?|quality)(?:[_ -]?(?:default|threshold))?\s*[:=]\s*\d+(?:\.\d+)?|\b(?:exactly\s+)?\d+(?:\.\d+)?\s*(?:attempts?|tries|次|轮)|固定\s*\d+(?:\.\d+)?\s*(?:%|次|轮|attempts?)/i;

async function loadJsonContract(name) {
  const filename = path.join(siteRoot, "data", name);
  try {
    return { value: JSON.parse(await readFile(filename, "utf8")), problems: [] };
  } catch (error) {
    return {
      value: null,
      problems: [`Content contract is missing or invalid at data/${name}: ${error.message}`],
    };
  }
}

function requireValue(value, label, problems) {
  if (value === undefined || value === null || value === "") {
    problems.push(`Missing ${label}.`);
  }
  return value;
}

function requireText(value, label, problems) {
  if (typeof value !== "string" || value.trim() === "") {
    problems.push(`Missing ${label}.`);
    return "";
  }
  return value;
}

function hasPinnedBlobLink(links, commit) {
  const pinnedBlobLink = new RegExp(`^https://github\\.com/.+/blob/${commit}/.+(?:#L\\d+(?:-L\\d+)?)?$`);
  return links.some((link) => typeof link === "string" && pinnedBlobLink.test(link));
}

function assertContract(problems) {
  assert.deepEqual(problems, [], `Content contract gaps:\n- ${problems.join("\n- ")}`);
}

async function textFiles(directory, extension) {
  try {
    const entries = await readdir(directory, { withFileTypes: true });
    const nested = await Promise.all(entries.map(async (entry) => {
      const filename = path.join(directory, entry.name);
      if (entry.isDirectory()) return textFiles(filename, extension);
      return entry.isFile() && filename.endsWith(extension) ? [filename] : [];
    }));
    return nested.flat();
  } catch {
    return [];
  }
}

async function sha256(filename) {
  return createHash("sha256").update(await readFile(filename)).digest("hex");
}

test("binds cross-Profile prohibitions to the clause that contains the routed action", () => {
  for (const action of [
    "路由", "编排", "调度", "回退", "故障转移", "评测", "部署", "实验", "执行计划", "回放", "学习", "灰度",
    "orchestration", "scheduling", "fallback", "failover", "experiment", "ExecutionPlan", "replay", "learning", "canary",
  ]) {
    assert.deepEqual(findAffirmativeCrossProfileClaims(`禁止跨 Profile ${action}`), []);
  }

  assert.deepEqual(
    findAffirmativeCrossProfileClaims("不能只支持单 Profile；本版允许跨 Profile 路由"),
    ["本版允许跨 Profile 路由"],
  );
  assert.deepEqual(
    findAffirmativeCrossProfileClaims("本版允许跨 Profile\ndeployment"),
    ["本版允许跨 Profile deployment"],
  );
  for (const action of [
    "route", "orchestration", "scheduling", "fallback", "failover", "evaluation", "deployment", "experiment", "ExecutionPlan", "replay", "learning", "canary",
  ]) {
    assert.deepEqual(
      findAffirmativeCrossProfileClaims(`This release supports cross-Profile ${action}.`),
      [`This release supports cross-Profile ${action}`],
    );
  }
  for (const action of ["实验", "计划", "回放", "学习", "灰度"]) {
    assert.deepEqual(
      findAffirmativeCrossProfileClaims(`本版允许跨 Profile ${action}`),
      [`本版允许跨 Profile ${action}`],
    );
  }
  assert.deepEqual(
    findAffirmativeCrossProfileClaims("ResearchComparison 是唯一允许跨 Profile 的值，但不能携带 ExecutionPlan"),
    [],
  );
  assert.deepEqual(
    findAffirmativeCrossProfileClaims("禁止跨 Profile route but supports cross-Profile deployment"),
    ["禁止跨 Profile route but supports cross-Profile deployment"],
  );
  assert.deepEqual(
    findAffirmativeCrossProfileClaims("禁止跨 Profile route 且拒绝跨 Profile deployment"),
    [],
  );
});

test("rejects Provider switching and fixed attempt or quality defaults in diagrams", () => {
  for (const unsafe of [
    "Provider A --> Provider B",
    "Providers A/B",
    "max_attempts=3",
    "maxAttempts=3",
    "exactly 3 attempts",
    "3 attempts",
    "quality_default=0.95",
    "固定 95%",
  ]) {
    assert.match(unsafe, forbiddenDiagramDesign);
  }
});

test("locks the twelve canonical chapter records", async () => {
  const { value: contract, problems } = await loadJsonContract("chapters.json");
  const chapters = requireValue(contract?.chapters, "chapters array", problems);
  if (Array.isArray(chapters)) {
    assert.deepEqual(chapters.map(({ slug }) => slug), expectedSlugs);
    assert.deepEqual(chapters, canonicalChapters, "Chapter records must use the canonical content contract exactly.");
  } else {
    problems.push("chapters must be an array containing the twelve canonical records.");
  }
  assertContract(problems);
});

test("locks baseline and competitor evidence metadata", async () => {
  const [baselineContract, evidenceContract] = await Promise.all([
    loadJsonContract("source-baselines.json"),
    loadJsonContract("competitor-evidence.json"),
  ]);
  const problems = [...baselineContract.problems, ...evidenceContract.problems];
  const baseline = requireValue(baselineContract.value?.runtime_baseline, "runtime_baseline", problems);
  if (baseline && typeof baseline === "object") {
    if (baseline.commit !== "ea13e527647cb701376c152f71086f01e68789ea") {
      problems.push("runtime_baseline.commit must pin the full source SHA.");
    }
    if (baseline.timestamp !== "2026-07-31T14:25:03+08:00") {
      problems.push("runtime_baseline.timestamp must pin the source timestamp.");
    }
    if (baseline.repository !== "https://github.com/Euphie/llm-proxy") {
      problems.push("runtime_baseline.repository must identify the runtime source repository.");
    }
    requireText(baseline.drift_state, "runtime_baseline.drift_state", problems);
    if (baseline.implementation_status !== "已实现") {
      problems.push("runtime_baseline.implementation_status must distinguish verified behavior as 已实现.");
    }
    if (baseline.evidence_level !== "本项目源码验证") {
      problems.push("runtime_baseline.evidence_level must be 本项目源码验证.");
    }
    const facts = baseline.verified_runtime_facts;
    if (!Array.isArray(facts) || facts.length !== 7) {
      problems.push("runtime_baseline.verified_runtime_facts must contain exactly seven facts.");
    } else {
      for (const [index, fact] of facts.entries()) {
        requireText(fact?.fact, `verified runtime fact ${index + 1} fact`, problems);
        requireText(fact?.conclusion, `verified runtime fact ${index + 1} conclusion`, problems);
        const permalink = fact && typeof fact === "object" ? fact.permalink : undefined;
        if (!hasPinnedBlobLink([permalink], "ea13e527647cb701376c152f71086f01e68789ea")) {
          problems.push(`Verified runtime fact ${index + 1} permalink must be a permanent blob link pinned to the source SHA.`);
        }
      }
    }
  } else if (baseline) {
    problems.push("runtime_baseline must be an object.");
  }

  const documentationBaseline = requireValue(
    baselineContract.value?.documentation_baseline,
    "documentation_baseline",
    problems,
  );
  if (documentationBaseline && typeof documentationBaseline === "object") {
    if (documentationBaseline.repository !== "codex/intelligent-routing-docs-site") {
      problems.push("documentation_baseline.repository must identify the documentation worktree.");
    }
    if (documentationBaseline.commit !== "039a060e502eca69f8c9da3b8fc1c0e42b503365") {
      problems.push("documentation_baseline.commit must pin the documentation starting SHA.");
    }
    if (!/implementation working tree/i.test(documentationBaseline.drift_state ?? "")) {
      problems.push("documentation_baseline.drift_state must identify an implementation working tree.");
    }
    requireText(documentationBaseline.timestamp, "documentation_baseline.timestamp", problems);
    if (documentationBaseline.implementation_status !== "已实现") {
      problems.push("documentation_baseline.implementation_status must describe the pinned documentation snapshot.");
    }
    if (documentationBaseline.evidence_level !== "本项目源码验证") {
      problems.push("documentation_baseline.evidence_level must be 本项目源码验证.");
    }
    if (documentationBaseline.verified_runtime_facts?.length !== 0) {
      problems.push("documentation_baseline must not claim runtime behavior.");
    }
  }

  const evidence = requireValue(evidenceContract.value, "competitor_evidence", problems);
  if (Array.isArray(evidence)) {
    if (evidence.length !== 10) {
      problems.push("competitor_evidence must contain six source and four documentation records.");
    }
    for (const [index, entry] of evidence.entries()) {
      requireText(entry?.native_mechanism, `competitor evidence ${index + 1} native_mechanism`, problems);
      requireText(entry?.mesotes_mapping, `competitor evidence ${index + 1} mesotes_mapping`, problems);
      if (!Array.isArray(entry?.adopt) || entry.adopt.length === 0) {
        problems.push(`Competitor evidence ${index + 1} adopt must be a non-empty array.`);
      }
      if (!Array.isArray(entry?.reject_or_defer) || entry.reject_or_defer.length === 0) {
        problems.push(`Competitor evidence ${index + 1} reject_or_defer must be a non-empty array.`);
      }
      if (!Array.isArray(entry?.permalinks) || entry.permalinks.length === 0) {
        problems.push(`Competitor evidence ${index + 1} permalinks must be a non-empty array.`);
      }
    }
    const sourceEvidence = evidence.filter((entry) => entry?.evidence_type === "source");
    if (sourceEvidence.length !== Object.keys(competitorCommits).length) {
      problems.push("competitor_evidence must contain exactly six source entries.");
    }
    for (const [competitor, commit] of Object.entries(competitorCommits)) {
      const entries = sourceEvidence.filter((entry) => entry?.competitor === competitor);
      if (entries.length !== 1) {
        problems.push(`Competitor ${competitor} must have exactly one source evidence entry.`);
        continue;
      }
      const [entry] = entries;
      if (entry.commit !== commit) {
        problems.push(`Competitor ${competitor} source evidence must pin commit ${commit}.`);
      }
      const links = [entry.permalink, ...(Array.isArray(entry.permalinks) ? entry.permalinks : [])];
      if (!hasPinnedBlobLink(links, commit)) {
        problems.push(`Competitor ${competitor} source evidence must include a permanent GitHub blob link pinned to ${commit}.`);
      }
      if (!Array.isArray(entry.permalinks) || !entry.permalinks.every((link) => hasPinnedBlobLink([link], commit))) {
        problems.push(`Every ${competitor} source permalink must pin ${commit}.`);
      }
    }
    const unexpectedSourceNames = sourceEvidence
      .map((entry) => entry?.competitor)
      .filter((competitor) => !Object.hasOwn(competitorCommits, competitor));
    if (unexpectedSourceNames.length > 0) {
      problems.push(`Unexpected competitor source evidence: ${unexpectedSourceNames.join(", ")}.`);
    }
    if (evidence.filter((entry) => entry?.evidence_type === "official_documentation").length !== 3) {
      problems.push("competitor_evidence must contain exactly three official_documentation entries.");
    }
    if (evidence.filter((entry) => entry?.evidence_type === "closed_documentation").length !== 1) {
      problems.push("competitor_evidence must contain exactly one closed_documentation entry.");
    }
  } else {
    problems.push("competitor_evidence must distinguish source, official_documentation, and closed_documentation evidence.");
  }
  assertContract(problems);
});

test("forbids unsafe terms and unsupported fixed claims across content and data", async () => {
  const files = [
    ...(await textFiles(path.join(siteRoot, "content"), ".md")),
    ...(await textFiles(path.join(siteRoot, "data"), ".json")),
  ];
  const corpus = (await Promise.all(files.map((filename) => readFile(filename, "utf8")))).join("\n");
  for (const forbidden of [
    /provider_kind/i, /provider_id/i,
    /固定\s*(?:98%|95%|20[–-]30%)/, /调用方凭据.*(?:异步|评测)/,
  ]) assert.doesNotMatch(corpus, forbidden);

  assert.match(
    corpus,
    /(?:禁止|拒绝|不可|不允许|不能|不得|不采用|不支持|绝不)[^。！？；;\n]*(?:跨\s*Profile)[^。！？；;\n]*(?:route|routing|fallback|evaluation|deployment|路由|编排|评测|部署)/i,
    "Content must explicitly state the cross-Profile boundary.",
  );
  assert.deepEqual(
    findAffirmativeCrossProfileClaims(corpus),
    [],
    "Content must not claim affirmative cross-Profile execution.",
  );
});

test("requires one upstream supplier and trust domain per Profile envelope", async () => {
  const pages = await Promise.all([
    readFile(path.join(siteRoot, "content", "core-model.md"), "utf8"),
    readFile(path.join(siteRoot, "content", "profile-isolation.md"), "utf8"),
  ]);
  for (const page of pages) {
    assert.match(page, /同一 Profile[^。\n]*多个[^。\n]*(?:账号|区域|endpoint|端点)[^。\n]*Target/i);
    assert.match(page, /同一(?:个)?上游(?:服务商|供应商)[^。\n]*同一[^。\n]*trust[- ]domain/i);
    assert.match(page, /不同上游(?:服务商|供应商)[^。\n]*必须[^。\n]*不同 Profile/i);
    assert.match(page, /独立[^。\n]*trust[- ]domain[^。\n]*必须[^。\n]*不同 Profile/i);
  }
  assert.match(pages[1], /调用方[^。\n]*Profile URL[^。\n]*显式选择/i);
});

test("requires chapter Markdown and diagram sources without aborting on missing files", async () => {
  const { value: contract, problems } = await loadJsonContract("chapters.json");
  const chapters = contract?.chapters;
  if (!Array.isArray(chapters)) {
    problems.push("Cannot verify chapter Markdown until data/chapters.json supplies chapters.");
  } else {
    for (const { slug } of chapters) {
      const filename = path.join(siteRoot, "content", `${slug}.md`);
      try {
        const markdown = await readFile(filename, "utf8");
        for (const reference of markdown.matchAll(/\]\((\/diagrams\/[^)#?]+\.svg)\)/g)) {
          const diagram = path.join(siteRoot, "public", reference[1]);
          try {
            await readFile(diagram);
          } catch {
            problems.push(`${slug}.md references missing local diagram ${reference[1]}.`);
          }
        }
      } catch {
        problems.push(`Missing target chapter Markdown: content/${slug}.md.`);
      }
    }
  }

  const [svgFiles, sourceFiles] = await Promise.all([
    textFiles(path.join(siteRoot, "public", "diagrams"), ".svg"),
    textFiles(path.join(siteRoot, "diagrams"), ".mmd"),
  ]);
  const expectedNames = canonicalDiagrams.map(([name]) => name).sort();
  assert.deepEqual(svgFiles.map((file) => path.basename(file, ".svg")).sort(), expectedNames);
  assert.deepEqual(sourceFiles.map((file) => path.basename(file, ".mmd")).sort(), expectedNames);

  for (const [name, chapterSlug] of canonicalDiagrams) {
    const sourceFile = path.join(siteRoot, "diagrams", `${name}.mmd`);
    const svgFile = path.join(siteRoot, "public", "diagrams", `${name}.svg`);
    const [source, svg, chapter] = await Promise.all([
      readFile(sourceFile, "utf8"),
      readFile(svgFile, "utf8"),
      readFile(path.join(siteRoot, "content", `${chapterSlug}.md`), "utf8"),
    ]);
    const sourceHash = await sha256(sourceFile);
    assert.match(source, /^\s*---[\s\S]*\baccTitle:/);
    assert.match(source, /\baccDescr:/);
    assert.match(svg, /<svg\b[^>]*role="graphics-document document"/);
    assert.match(svg, /<title\b[^>]*>[^<]+<\/title>/);
    assert.match(svg, /<desc\b[^>]*>[^<]+<\/desc>/);
    assert.match(svg, new RegExp(`data-source-sha256="${sourceHash}"`));
    assert.match(chapter, new RegExp(`!\\[[^\\]]+\\]\\(/diagrams/${name}\\.svg\\)\\n\\n图示等价说明`));
    assert.doesNotMatch(source, forbiddenDiagramDesign);
  }
  assertContract(problems);
});

test("preserves Mesotes marks and binds social assets to the Profile-local design", async () => {
  assert.equal(
    await sha256(path.join(siteRoot, "public", "brand", "mesotes-mark.png")),
    "1346c64b3e922da4fca558d13f8d1f41c355af620c083e95ba52c014de60cff7",
  );
  assert.equal(
    await sha256(path.join(siteRoot, "public", "brand", "mesotes-mark-256.png")),
    "d21f8f30a820d5cd1a35ee877c6fc048354da92f6d4c03d2597373b7bbf072cf",
  );

  const [ogPng, ogSource, favicon] = await Promise.all([
    readFile(path.join(siteRoot, "public", "og-routing-design.png")),
    readFile(path.join(siteRoot, "assets", "og-routing-design.svg"), "utf8"),
    readFile(path.join(siteRoot, "public", "favicon.svg"), "utf8"),
  ]);
  assert.equal(ogPng.readUInt32BE(16), 1200, "OG image must be 1200px wide.");
  assert.equal(ogPng.readUInt32BE(20), 630, "OG image must be 630px high.");
  assert.notEqual(
    createHash("sha256").update(ogPng).digest("hex"),
    "6261f2f9def15c21d95cfdccb750f74c2f00c5a77dff5e97f732172ed3977d10",
    "OG image must not retain the legacy llm-proxy artwork.",
  );
  assert.match(ogSource, /MESOTES/);
  assert.match(ogSource, /Profile-local intelligent routing/);
  assert.match(ogSource, /Resolve Profile[\s\S]*Model Router[\s\S]*Target Scheduler[\s\S]*Same-Profile Targets/);
  assert.doesNotMatch(ogSource, /llm-proxy|provider_kind|provider_id|cross-Profile/i);

  const { default: sharp } = await import("sharp");
  const normalize = (input) => sharp(input)
    .resize(120, 63, { fit: "fill" })
    .flatten({ background: "#ffffff" })
    .blur(1)
    .raw()
    .toBuffer();
  const [sourcePixels, publicPixels] = await Promise.all([
    normalize(Buffer.from(ogSource)),
    normalize(ogPng),
  ]);
  const meanAbsoluteDifference = sourcePixels.reduce(
    (total, value, index) => total + Math.abs(value - publicPixels[index]),
    0,
  ) / sourcePixels.length;
  assert.ok(
    meanAbsoluteDifference < 5,
    `Public OG image drifted from its SVG source (mean pixel delta ${meanAbsoluteDifference.toFixed(3)}).`,
  );

  assert.match(favicon, /<svg\b[^>]*aria-labelledby="title"/);
  assert.match(favicon, /<title id="title">Mesotes<\/title>/);
  assert.equal((favicon.match(/<rect x="-5" y="-27"/g) ?? []).length, 8);
  for (const color of ["#05345f", "#ec5a45", "#087e88", "#0b6fc2"]) {
    assert.match(favicon, new RegExp(color, "i"));
  }
  assert.doesNotMatch(
    favicon,
    /llm-proxy|<script|<foreignObject|\son[a-z]+\s*=|javascript:|\b(?:href|src)\s*=|url\s*\(/i,
  );
});

test("keeps light-theme links and compact status labels above WCAG AA contrast", async () => {
  const css = await readFile(path.join(siteRoot, "app", "globals.css"), "utf8");
  const luminance = (channels) => {
    const [red, green, blue] = channels.map((channel) => {
      const value = channel / 255;
      return value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4;
    });
    return 0.2126 * red + 0.7152 * green + 0.0722 * blue;
  };
  const contrast = (foreground, background) => {
    const lighter = Math.max(luminance(foreground), luminance(background));
    const darker = Math.min(luminance(foreground), luminance(background));
    return (lighter + 0.05) / (darker + 0.05);
  };
  const hexChannels = (hex) => [1, 3, 5].map((index) => Number.parseInt(hex.slice(index, index + 2), 16));
  const accent = /:root\s*{[\s\S]*?--accent:\s*(#[0-9a-f]{6})/i.exec(css)?.[1];
  assert.ok(accent);
  assert.ok(contrast(hexChannels(accent), [255, 255, 255]) >= 4.5);

  const rootTokens = /:root\s*{([^}]*)}/i.exec(css)?.[1] ?? "";
  const darkTokens = /html\[data-docs-theme="dark"\]\s*{([^}]*)}/i.exec(css)?.[1] ?? "";
  const codeFocus = /--code-focus:\s*(#[0-9a-f]{6})/i.exec(rootTokens)?.[1];
  const lightCodeBackground = /--code-bg:\s*(#[0-9a-f]{6})/i.exec(rootTokens)?.[1];
  const darkCodeBackground = /--code-bg:\s*(#[0-9a-f]{6})/i.exec(darkTokens)?.[1];
  assert.ok(codeFocus && lightCodeBackground && darkCodeBackground);
  for (const background of [lightCodeBackground, darkCodeBackground]) {
    assert.ok(
      contrast(hexChannels(codeFocus), hexChannels(background)) >= 3,
      "Code focus indicator must meet WCAG non-text contrast in both themes.",
    );
  }

  for (const status of ["implemented", "target", "future"]) {
    const block = new RegExp(`\\.status-badge--${status}\\s*\\{([^}]*)\\}`, "i").exec(css)?.[1] ?? "";
    const color = /color:\s*(#[0-9a-f]{6})/i.exec(block)?.[1];
    const background = /background:\s*rgba\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)\s*,\s*([\d.]+)\s*\)/i.exec(block);
    assert.ok(color && background, `Missing light-theme ${status} badge colors.`);
    const alpha = Number.parseFloat(background[4]);
    const blended = background.slice(1, 4).map((channel) => (
      Number.parseInt(channel, 10) * alpha + 255 * (1 - alpha)
    ));
    assert.ok(
      contrast(hexChannels(color), blended) >= 4.5,
      `${status} badge text must meet WCAG AA contrast.`,
    );
  }
});

test("keeps Mermaid text and required node boundaries above WCAG contrast", async () => {
  const luminance = (hex) => {
    const [red, green, blue] = [1, 3, 5].map((index) => {
      const value = Number.parseInt(hex.slice(index, index + 2), 16) / 255;
      return value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4;
    });
    return 0.2126 * red + 0.7152 * green + 0.0722 * blue;
  };
  const contrast = (left, right) => {
    const lighter = Math.max(luminance(left), luminance(right));
    const darker = Math.min(luminance(left), luminance(right));
    return (lighter + 0.05) / (darker + 0.05);
  };
  const canvas = "#f4f9fc";

  for (const [name] of canonicalDiagrams) {
    const [source, svg] = await Promise.all([
      readFile(path.join(siteRoot, "diagrams", `${name}.mmd`), "utf8"),
      readFile(path.join(siteRoot, "public", "diagrams", `${name}.svg`), "utf8"),
    ]);
    const palette = [
      ...source.matchAll(/classDef\s+([\w-]+)\s+fill:(#[0-9a-f]{6}),color:(#[0-9a-f]{6}),stroke:(#[0-9a-f]{6})/gi),
      ...source.matchAll(/style\s+PROFILE\s+fill:(#[0-9a-f]{6}),stroke:(#[0-9a-f]{6})[^\n]*color:(#[0-9a-f]{6})/gi),
    ].map((match) => (
      match[0].startsWith("style")
        ? { label: "PROFILE", fill: match[1], stroke: match[2], text: match[3] }
        : { label: match[1], fill: match[2], text: match[3], stroke: match[4] }
    ));
    assert.ok(palette.length > 0, `${name}.mmd must declare an auditable palette.`);

    for (const item of palette) {
      assert.ok(
        contrast(item.text, item.fill) >= 4.5,
        `${name}:${item.label} text must meet WCAG AA contrast.`,
      );
      assert.ok(
        Math.max(contrast(item.fill, canvas), contrast(item.stroke, item.fill)) >= 3,
        `${name}:${item.label} boundary must meet WCAG non-text contrast.`,
      );
      for (const color of [item.fill, item.text, item.stroke]) {
        assert.ok(svg.toLowerCase().includes(color.toLowerCase()), `${name}.svg is missing ${color}.`);
      }
    }
  }
});
