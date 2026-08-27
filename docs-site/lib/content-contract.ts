export type ImplementationStatus = "已实现";
export const evidenceLevels = ["本项目源码与测试验证"] as const;
export type EvidenceLevel = (typeof evidenceLevels)[number];

const evidenceLevelSet = new Set<string>(evidenceLevels);

export function isEvidenceLevel(value: unknown): value is EvidenceLevel {
  return typeof value === "string" && evidenceLevelSet.has(value);
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
    const nearest = [...claimPrefix.matchAll(prohibitionPattern)].at(-1);
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
