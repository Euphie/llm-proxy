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
  ["overview", "智能路由概览", "用一个简单流程说明 auto 如何在单个 Profile 内选模型、控成本并守住质量。", "理解方案", "compass", "blue", "本版目标", "本项目源码验证"],
  ["source-baseline", "当前能力与目标", "区分当前已实现能力与智能路由的待实现目标。", "理解方案", "layers", "cyan", "本版目标", "本项目源码验证"],
  ["competitor-evidence", "参考方案与取舍", "提炼竞品中值得采用的机制，并明确不照搬的复杂设计。", "理解方案", "activity", "orange", "本版目标", "竞品源码验证"],
  ["core-model", "Profile 配置与模型角色", "说明启用 auto 所需配置，以及 ModelCard、Target 和模型角色。", "理解方案", "sliders", "indigo", "本版目标", "竞品源码验证"],
  ["profile-isolation", "Profile 隔离边界", "URL 选定 Profile 后，路由、视觉、重试和评测始终留在该边界内。", "运行时", "shield", "red", "本版目标", "本项目源码验证"],
  ["online-routing", "在线路由", "规则先判断，拿不准时调用轻量任务分析器，再生成不可变执行计划。", "运行时", "route", "green", "本版目标", "竞品源码验证"],
  ["quality-and-cost", "质量与成本", "先满足 Route 质量门槛，再选择完整成本更低的模型。", "运行时", "scale", "yellow", "本版目标", "竞品源码验证"],
  ["target-reliability", "Target 与容错", "在同一 Profile 内选择模型部署，并以有界重试处理临时故障。", "运行时", "activity", "teal", "已实现", "本项目源码与测试验证"],
  ["runtime-reliability", "视觉、重试与 Session", "把视觉辅助、尝试预算、流式提交和 Session 连续性放进同一执行边界。", "运行时", "shield", "slate", "本版目标", "竞品源码验证"],
  ["evaluation-feedback", "异步评测与策略优化", "用异步样本评测积累证据，只生成候选策略，不自动改变线上选择。", "控制面", "activity", "orange", "已实现", "本项目源码与测试验证"],
  ["strategy-lifecycle", "策略生命周期", "以不可变版本、CAS、灰度和 LKG 安全发布或回滚策略。", "控制面", "git-branch", "indigo", "已实现", "本项目源码与测试验证"],
  ["engineering-and-delivery", "实施范围与交付", "明确 v1 范围、实施阶段和验收条件。", "交付", "database", "slate", "本版目标", "本项目源码验证"],
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
    findAffirmativeCrossProfileClaims("禁止跨 Profile route but supports cross-Profile deployment"),
    ["禁止跨 Profile route but supports cross-Profile deployment"],
  );
  assert.deepEqual(
    findAffirmativeCrossProfileClaims("禁止跨 Profile route 且拒绝跨 Profile deployment"),
    [],
  );
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
    if (baseline.commit !== "81a2fc457c69323fc3e5bd63f68da9191bc9b587") {
      problems.push("runtime_baseline.commit must pin the full source SHA.");
    }
    if (baseline.timestamp !== "2026-08-02T01:53:01+08:00") {
      problems.push("runtime_baseline.timestamp must pin the source timestamp.");
    }
    if (baseline.repository !== "https://github.com/Euphie/llm-proxy") {
      problems.push("runtime_baseline.repository must identify the runtime source repository.");
    }
    requireText(baseline.drift_state, "runtime_baseline.drift_state", problems);
    if (baseline.implementation_status !== "Phase 1 已实现") {
      problems.push("runtime_baseline.implementation_status must identify Phase 1 as implemented.");
    }
    if (baseline.evidence_level !== "本项目源码与测试验证") {
      problems.push("runtime_baseline.evidence_level must include source and test verification.");
    }
    const facts = baseline.verified_runtime_facts;
    if (!Array.isArray(facts) || facts.length !== 7) {
      problems.push("runtime_baseline.verified_runtime_facts must contain exactly seven facts.");
    } else {
      for (const [index, fact] of facts.entries()) {
        requireText(fact?.fact, `verified runtime fact ${index + 1} fact`, problems);
        requireText(fact?.conclusion, `verified runtime fact ${index + 1} conclusion`, problems);
        const permalink = fact && typeof fact === "object" ? fact.permalink : undefined;
        if (!hasPinnedBlobLink([permalink], "81a2fc457c69323fc3e5bd63f68da9191bc9b587")) {
          problems.push(`Verified runtime fact ${index + 1} permalink must be a permanent blob link pinned to the source SHA.`);
        }
      }
    }
  } else if (baseline) {
    problems.push("runtime_baseline must be an object.");
  }

  if (baselineContract.value && Object.keys(baselineContract.value).join(",") !== "runtime_baseline") {
    problems.push("source-baselines.json must contain only the runtime source baseline.");
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

test("forbids affirmative cross-Profile execution", async () => {
  const files = [
    path.join(siteRoot, "..", "docs", "intelligent-routing.md"),
    ...(await textFiles(path.join(siteRoot, "content"), ".md")),
    ...(await textFiles(path.join(siteRoot, "data"), ".json")),
    ...(await textFiles(path.join(siteRoot, "diagrams"), ".mmd")),
  ];
  const corpus = (await Promise.all(files.map((filename) => readFile(filename, "utf8")))).join("\n");
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

test("locks the approved intelligent-routing contracts", async () => {
  const readPage = (slug) => readFile(path.join(siteRoot, "content", `${slug}.md`), "utf8");
  const [overview, core, isolation, routing, quality, target, runtime, evaluation, lifecycle, delivery] = await Promise.all([
    readPage("overview"), readPage("core-model"), readPage("profile-isolation"), readPage("online-routing"),
    readPage("quality-and-cost"), readPage("target-reliability"), readPage("runtime-reliability"),
    readPage("evaluation-feedback"), readPage("strategy-lifecycle"), readPage("engineering-and-delivery"),
  ]);

  assert.match(overview, /model=auto[\s\S]*参与模型[\s\S]*强模型基线[\s\S]*轻量任务分析器[\s\S]*有效策略配置/);
  assert.match(core, /新增模型默认不加入/);
  assert.match(core, /一个 Profile 同时只有一个 active Strategy[\s\S]*多个 Route[\s\S]*默认 Route[\s\S]*统一 Request\/AttemptBudget[\s\S]*Route 映射前创建/);
  assert.match(core, /context_window[\s\S]*client_context_window/);
  assert.match(core, /Upstream 是支持全部目录模型的 primary[\s\S]*备用 Target/);
  assert.match(core, /未登记或未确认的能力不满足对应硬约束[\s\S]*缺少计算完整成本所需的价格字段[\s\S]*不能发布[\s\S]*价格为零是有效值/);
  assert.match(isolation, /Profile 是永久路由边界[\s\S]*不能到另一个 Profile 寻找候选/);
  assert.match(isolation, /逻辑隔离合同[\s\S]*共享无状态代码和 SQLite 是允许的/);
  assert.match(routing, /本地规则优先[\s\S]*无法稳定判断[\s\S]*轻量任务分析器/);
  assert.match(routing, /Anthropic `POST \/v1\/messages`[\s\S]*OpenAI `POST \/v1\/chat\/completions`、`POST \/v1\/responses`[\s\S]*unsupported operation/);
  assert.match(routing, /任务类型按 active 策略映射到 Route[\s\S]*默认 Route/);
  assert.match(routing, /入口[\s\S]*预检[\s\S]*高风险请求只检查强模型基线[\s\S]*其他请求[\s\S]*候选硬约束过滤/);
  assert.match(routing, /失败、超时或置信度不足[\s\S]*强模型基线/);
  assert.match(routing, /高风险[\s\S]*结构信号[\s\S]*场景规则[\s\S]*分析器判断/);
  assert.match(routing, /不可变 ExecutionPlan/);
  assert.match(quality, /Route 质量门槛[\s\S]*完整成本/);
  assert.match(quality, /带时间衰减的保守估计[\s\S]*样本权重更高/);
  assert.match(quality, /证据不足时使用保守先验[\s\S]*置信下界[\s\S]*强模型基线[\s\S]*否则请求失败/);
  assert.match(quality, /A\/B\/C\/D 等级[\s\S]*只用于展示/);
  assert.match(target, /Upstream 是 primary[\s\S]*按配置顺序切换同模型备用 Target/);
  assert.match(runtime, /主模型支持视觉[\s\S]*主模型不支持视觉且当前 Profile 已开启增强[\s\S]*该模型从候选中排除/);
  for (const field of [
    "max_answer_attempts", "max_auxiliary_calls", "max_total_outbound_calls", "max_retries_per_target",
    "max_target_switches", "max_model_switches", "deadline", "max_worst_case_cost",
  ]) assert.match(runtime, new RegExp(field));
  assert.match(runtime, /ClientCommit 后[\s\S]*不再重试|提交后禁止重试/);
  assert.match(runtime, /Session 绑定只对 `model=auto` 生效[\s\S]*永久不足[\s\S]*升级[\s\S]*临时 429、5xx[\s\S]*不改变 Session 绑定[\s\S]*不自动降级/);
  assert.match(runtime, /任务分析先消耗预算[\s\S]*ExecutionPlan 固定剩余预算[\s\S]*异步评测不占用已结束的在线请求预算/);
  assert.match(runtime, /视觉缓存键至少包含 Profile[\s\S]*当前问题上下文[\s\S]*鉴权域/);
  assert.match(runtime, /Session 键使用 HMAC[\s\S]*原始 Session ID[\s\S]*Profile[\s\S]*Route[\s\S]*鉴权域[\s\S]*缺少有效 Session ID[\s\S]*不建立跨请求绑定/);
  assert.match(evaluation, /默认关闭/);
  assert.match(evaluation, /有界内存队列[\s\S]*队列满时直接丢弃/);
  assert.match(evaluation, /透传凭据[\s\S]*最长 10 分钟[\s\S]*不得把凭据、prompt、图片或完整输出写入 SQLite/);
  assert.match(evaluation, /候选策略[\s\S]*不能自动把候选变成 active/);
  assert.match(evaluation, /视觉描述缓存与评测统计相互独立[\s\S]*完整键一致[\s\S]*跨请求复用/);
  assert.match(lifecycle, /20260801-001[\s\S]*别名/);
  assert.match(lifecycle, /CAS[\s\S]*热加载[\s\S]*last-known-good/);
  assert.match(delivery, /单进程、单副本[\s\S]*SQLite/);
  assert.match(delivery, /第一阶段[\s\S]*第二阶段[\s\S]*第三阶段/);
});

test("keeps the canonical repository design aligned with the documentation site", async () => {
  const design = await readFile(path.join(siteRoot, "..", "docs", "intelligent-routing.md"), "utf8");

  assert.match(design, /状态：Phase 1[^\n]*已实现/);
  assert.match(design, /model=auto[\s\S]*一次请求只能使用 URL 选中的 Profile/);
  assert.match(design, /本地规则[\s\S]*轻量任务分析模型[\s\S]*强模型基线满足全部硬约束/);
  assert.match(design, /Anthropic `POST \/v1\/messages`[\s\S]*OpenAI[\s\S]*`POST \/v1\/chat\/completions`[\s\S]*`POST \/v1\/responses`[\s\S]*unsupported operation/);
  assert.match(design, /任务类型按已发布策略映射到一个 Route[\s\S]*默认 Route/);
  assert.match(design, /主模型支持视觉[\s\S]*主模型不支持视觉[\s\S]*该主模型不能成为候选/);
  assert.match(design, /max_answer_attempts[\s\S]*max_worst_case_cost/);
  assert.match(design, /Session 绑定只对 `model=auto` 生效[\s\S]*不会自动把会话降回较弱模型/);
  assert.match(design, /会话键使用 HMAC[\s\S]*原始 Session ID[\s\S]*Profile[\s\S]*Route[\s\S]*鉴权域[\s\S]*不建立跨请求绑定/);
  assert.match(design, /有界内存队列[\s\S]*不修改 active 策略/);
  assert.match(design, /最多在内存保留凭据 10 分钟/);
  assert.match(design, /YYYYMMDD-NNN[\s\S]*原子热加载[\s\S]*CAS/);
  assert.match(design, /context_window[\s\S]*client_context_window/);
  assert.match(design, /未登记或未确认的能力不视为满足对应硬约束[\s\S]*缺少计算完整成本所需的价格字段[\s\S]*不能发布/);
  assert.match(design, /质量证据不足时使用保守先验[\s\S]*置信下界[\s\S]*强模型基线[\s\S]*否则请求失败/);
  assert.match(design, /单进程、单副本[\s\S]*不做分布式协调/);
  assert.doesNotMatch(
    design,
    /evaluation_credential_ref|credential_ref|envelope_sha|tombstone|ResearchComparison|Payload Replay|decision_shadow|evaluation_shadow/i,
  );
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
  }
  assertContract(problems);
});

test("preserves Mesotes marks and binds social assets to the Profile-local design", async () => {
  const [markPng, compactMarkPng, ogPng, ogSource, favicon] = await Promise.all([
    readFile(path.join(siteRoot, "public", "brand", "mesotes-mark.png")),
    readFile(path.join(siteRoot, "public", "brand", "mesotes-mark-256.png")),
    readFile(path.join(siteRoot, "public", "og-routing-design.png")),
    readFile(path.join(siteRoot, "assets", "og-routing-design.svg"), "utf8"),
    readFile(path.join(siteRoot, "public", "favicon.svg"), "utf8"),
  ]);
  assert.equal(markPng.readUInt32BE(16), 1254, "Primary mark must remain 1254px square.");
  assert.equal(markPng.readUInt32BE(20), 1254, "Primary mark must remain 1254px square.");
  assert.equal(compactMarkPng.readUInt32BE(16), 256, "Compact mark must remain 256px square.");
  assert.equal(compactMarkPng.readUInt32BE(20), 256, "Compact mark must remain 256px square.");
  assert.equal(ogPng.readUInt32BE(16), 1200, "OG image must be 1200px wide.");
  assert.equal(ogPng.readUInt32BE(20), 630, "OG image must be 630px high.");
  assert.notEqual(
    createHash("sha256").update(ogPng).digest("hex"),
    "6261f2f9def15c21d95cfdccb750f74c2f00c5a77dff5e97f732172ed3977d10",
    "OG image must not retain the legacy llm-proxy artwork.",
  );
  assert.match(ogSource, /MESOTES/);
  assert.equal((ogSource.match(/class="brand-arm"/g) ?? []).length, 3);
  assert.match(ogSource, /Profile-local intelligent routing/);
  assert.match(ogSource, /Resolve Profile[\s\S]*Rules \+ Analyzer[\s\S]*Quality Gate[\s\S]*Execution Plan/);
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
  assert.equal((favicon.match(/class="route-arm"/g) ?? []).length, 3);
  assert.doesNotMatch(favicon, /<rect x="-5" y="-27"/);
  for (const color of ["#05345f", "#ec5a45", "#087e88", "#0b6fc2"]) {
    assert.match(favicon, new RegExp(color, "i"));
  }
  assert.doesNotMatch(
    favicon,
    /llm-proxy|<script|<foreignObject|\son[a-z]+\s*=|javascript:|\b(?:href|src)\s*=/i,
  );
  const paintReferences = [...favicon.matchAll(/url\(([^)]+)\)/gi)].map(([, reference]) => reference.trim());
  assert.ok(paintReferences.length > 0);
  assert.ok(paintReferences.every((reference) => /^#[a-z0-9_-]+$/i.test(reference)));

  const renderMark = (size) => sharp(Buffer.from(favicon), { density: 384 })
    .resize(size, size, { fit: "fill" })
    .ensureAlpha()
    .raw()
    .toBuffer();
  const readMark = (input) => sharp(input).ensureAlpha().raw().toBuffer();
  const [expectedMark, actualMark, expectedCompactMark, actualCompactMark] = await Promise.all([
    renderMark(1254),
    readMark(markPng),
    renderMark(256),
    readMark(compactMarkPng),
  ]);
  assert.deepEqual(actualMark, expectedMark, "Primary PNG must be rendered from the canonical favicon mark.");
  assert.deepEqual(actualCompactMark, expectedCompactMark, "Compact PNG must be rendered from the canonical favicon mark.");
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
