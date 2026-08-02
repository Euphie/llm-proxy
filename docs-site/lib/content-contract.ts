export type ImplementationStatus = "已实现" | "本版目标" | "后续方向";
export const evidenceLevels = [
  "本项目源码验证",
  "本项目源码与测试验证",
  "竞品源码验证",
  "官方文档验证",
  "闭源产品参考",
] as const;
export type EvidenceLevel = (typeof evidenceLevels)[number];

const evidenceLevelSet = new Set<string>(evidenceLevels);

export function isEvidenceLevel(value: unknown): value is EvidenceLevel {
  return typeof value === "string" && evidenceLevelSet.has(value);
}

export interface SourceBaseline {
  repository: string;
  commit: string;
  timestamp: string;
  drift_state: string;
  implementation_status: ImplementationStatus;
  evidence_level: EvidenceLevel;
  verified_runtime_facts: Array<{
    fact: string;
    conclusion: string;
    permalink: string;
  }>;
}

export interface CompetitorEvidence {
  competitor: string;
  evidence_type: "source" | "official_documentation" | "closed_documentation";
  evidence_level: EvidenceLevel;
  commit?: string;
  native_mechanism: string;
  mesotes_mapping: string;
  adopt: string[];
  reject_or_defer: string[];
  permalinks: string[];
}

export interface SourceBaselines {
  runtime_baseline: SourceBaseline;
}

export interface EvidenceMarkerData {
  sourceBaselines: SourceBaselines;
  competitorEvidence: CompetitorEvidence[];
}

const crossProfileSource = "(?:跨\\s*Profile|cross[-\\s]*Profile)";
const crossProfileActionSource = "(?:route|routing|orchestrat(?:e|ion)|schedul(?:e|ing)|fallback|failover|evaluat(?:e|ion)|deploy(?:ment)?|experiments?|execution[-_\\s]*plans?|plans?|replays?|learn(?:ing)?|canary|路由|编排|调度|回退|故障转移|评测|部署|实验|执行计划|计划|回放|学习|灰度)";
const prohibitionPattern = /(?:禁止|拒绝|不可|不允许|不能|不得|不采用|不支持|绝不|must\s+not(?:\s+(?:allow|support|adopt|enable|execute|deploy))?|cannot(?:\s+(?:allow|support|adopt|enable|execute|deploy))?|do\s+not\s+(?:allow|support|adopt|enable|execute|deploy)|does\s+not\s+(?:allow|support|adopt|enable|execute|deploy)|never\s+(?:allow|support|adopt|enable|execute|deploy))/gi;
const affirmativePattern = /(?:允许|支持|采用|启用|可以|allow|support|adopt|enable|execute)/i;

function clauseHasAffirmativeCrossProfileClaim(clause: string): boolean {
  const crossProfiles = [...clause.matchAll(new RegExp(crossProfileSource, "gi"))];
  let claimStart = 0;

  for (const [index, crossProfile] of crossProfiles.entries()) {
    const crossStart = crossProfile.index ?? 0;
    const crossEnd = crossStart + crossProfile[0].length;
    const nextCrossStart = crossProfiles[index + 1]?.index ?? clause.length;
    const beforeCross = clause.slice(claimStart, crossStart);
    const afterCross = clause.slice(crossEnd, nextCrossStart);
    const actionBefore = [...beforeCross.matchAll(new RegExp(crossProfileActionSource, "gi"))].at(-1);
    const actionAfter = new RegExp(crossProfileActionSource, "i").exec(afterCross);

    if (!actionBefore && !actionAfter) {
      claimStart = crossEnd;
      continue;
    }

    const claimPrefix = actionAfter
      ? `${beforeCross}${afterCross.slice(0, actionAfter.index ?? 0)}`
      : beforeCross;
    const prohibitions = [...claimPrefix.matchAll(prohibitionPattern)];
    const nearest = prohibitions.at(-1);
    if (!nearest) return true;
    const afterProhibition = claimPrefix.slice((nearest.index ?? 0) + nearest[0].length);
    if (affirmativePattern.test(afterProhibition)) return true;

    claimStart = actionAfter
      ? crossEnd + (actionAfter.index ?? 0) + actionAfter[0].length
      : crossEnd;
  }
  return false;
}

export function findAffirmativeCrossProfileClaims(value: string): string[] {
  return value
    .replace(/\s+/g, " ")
    .split(/[。！？!?；;，,.]+/)
    .map((clause) => clause.trim())
    .filter(clauseHasAffirmativeCrossProfileClaim);
}

function cell(value: string): string {
  return value.replace(/\|/g, "\\|").replace(/\r?\n/g, " ");
}

function link(label: string, href: string): string {
  return `[${cell(label)}](${href})`;
}

function sourceBaselineTable(baselines: SourceBaselines): string {
  const baseline = baselines.runtime_baseline;

  return [
    "| 基线 | 仓库 | 固定提交 | 采集时间 | 漂移状态 | 实现状态 | 证据等级 |",
    "| --- | --- | --- | --- | --- | --- | --- |",
    [
      "运行时",
      cell(baseline.repository),
      `\`${cell(baseline.commit)}\``,
      cell(baseline.timestamp),
      cell(baseline.drift_state),
      cell(baseline.implementation_status),
      cell(baseline.evidence_level),
    ].join(" | ").replace(/^/, "| ").replace(/$/, " |"),
  ].join("\n");
}

function sourceFactsTable(baseline: SourceBaseline): string {
  return [
    "| 已验证运行时事实 | 结论 | 固定源码 |",
    "| --- | --- | --- |",
    ...baseline.verified_runtime_facts.map((fact) => (
      `| ${cell(fact.fact)} | ${cell(fact.conclusion)} | ${link("源码", fact.permalink)} |`
    )),
  ].join("\n");
}

function competitorEvidenceTable(evidence: CompetitorEvidence[]): string {
  return [
    "| 产品 | 原生机制 | Mesotes 有界映射 | 采用 | 拒绝或推迟 | 证据 |",
    "| --- | --- | --- | --- | --- | --- |",
    ...evidence.map((entry) => {
      const sources = entry.permalinks.map((href, index) => link(`证据 ${index + 1}`, href)).join("、");
      return [
        entry.competitor,
        entry.native_mechanism,
        entry.mesotes_mapping,
        entry.adopt.join("；"),
        entry.reject_or_defer.join("；"),
        `${entry.evidence_level}：${sources}`,
      ].map(cell).join(" | ").replace(/^/, "| ").replace(/$/, " |");
    }),
  ].join("\n");
}

export function expandEvidenceMarkers(markdown: string, data: EvidenceMarkerData): string {
  const replacements: Record<string, string> = {
    SOURCE_BASELINE: sourceBaselineTable(data.sourceBaselines),
    SOURCE_FACTS: sourceFactsTable(data.sourceBaselines.runtime_baseline),
    COMPETITOR_EVIDENCE: competitorEvidenceTable(data.competitorEvidence),
  };

  const expanded = markdown.replace(/\{\{\s*([A-Z][A-Z0-9_]*)\s*\}\}/g, (marker, name: string) => (
    Object.hasOwn(replacements, name) ? replacements[name] : marker
  ));
  const unknown = /\{\{\s*([A-Z][A-Z0-9_]*)\s*\}\}/.exec(expanded);
  if (unknown) {
    throw new Error(`Unknown content contract marker: ${unknown[1]}`);
  }
  return expanded;
}
