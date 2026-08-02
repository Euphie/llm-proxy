import competitorEvidenceMarkdown from "../content/competitor-evidence.md?raw";
import coreModelMarkdown from "../content/core-model.md?raw";
import engineeringAndDeliveryMarkdown from "../content/engineering-and-delivery.md?raw";
import evaluationFeedbackMarkdown from "../content/evaluation-feedback.md?raw";
import onlineRoutingMarkdown from "../content/online-routing.md?raw";
import overviewMarkdown from "../content/overview.md?raw";
import profileIsolationMarkdown from "../content/profile-isolation.md?raw";
import qualityAndCostMarkdown from "../content/quality-and-cost.md?raw";
import runtimeReliabilityMarkdown from "../content/runtime-reliability.md?raw";
import sourceBaselineMarkdown from "../content/source-baseline.md?raw";
import strategyLifecycleMarkdown from "../content/strategy-lifecycle.md?raw";
import targetReliabilityMarkdown from "../content/target-reliability.md?raw";
import chapterManifest from "../data/chapters.json";
import competitorEvidence from "../data/competitor-evidence.json";
import sourceBaselines from "../data/source-baselines.json";
import {
  expandEvidenceMarkers,
  isEvidenceLevel,
  type CompetitorEvidence,
  type EvidenceLevel,
  type ImplementationStatus,
  type SourceBaselines,
} from "./content-contract";
import {
  buildSearchEntries,
  contentVersion,
  extractMarkdownHeadings,
  readingTimeMinutes,
  type SearchEntry,
} from "./search";
import {
  chapterGroups,
  type ChapterGroup,
  type ChapterNavigationItem,
} from "./chapter-model";

export interface TableOfContentsItem {
  id: string;
  title: string;
  level: 2 | 3;
  line: number;
}

export interface Chapter {
  slug: string;
  index: number;
  title: string;
  shortTitle: string;
  summary: string;
  group: ChapterGroup;
  icon: string;
  accent: string;
  implementationStatus: ImplementationStatus;
  evidenceLevel: EvidenceLevel;
  content: string;
  tableOfContents: TableOfContentsItem[];
  readingMinutes: number;
}

interface ManifestChapter {
  slug: string;
  title: string;
  summary: string;
  group: ChapterGroup;
  icon: string;
  accent: string;
  implementation_status: ImplementationStatus;
  evidence_level: EvidenceLevel;
}

const markdownBySlug: Record<string, string> = {
  overview: overviewMarkdown,
  "source-baseline": sourceBaselineMarkdown,
  "competitor-evidence": competitorEvidenceMarkdown,
  "core-model": coreModelMarkdown,
  "profile-isolation": profileIsolationMarkdown,
  "online-routing": onlineRoutingMarkdown,
  "quality-and-cost": qualityAndCostMarkdown,
  "target-reliability": targetReliabilityMarkdown,
  "runtime-reliability": runtimeReliabilityMarkdown,
  "evaluation-feedback": evaluationFeedbackMarkdown,
  "strategy-lifecycle": strategyLifecycleMarkdown,
  "engineering-and-delivery": engineeringAndDeliveryMarkdown,
};

const implementationStatuses = new Set<ImplementationStatus>(["已实现", "本版目标", "后续方向"]);
function manifestChapter(value: (typeof chapterManifest.chapters)[number]): ManifestChapter {
  if (
    !chapterGroups.includes(value.group as ChapterGroup) ||
    !implementationStatuses.has(value.implementation_status as ImplementationStatus) ||
    !isEvidenceLevel(value.evidence_level)
  ) {
    throw new Error(`Invalid chapter manifest entry: ${value.slug}`);
  }
  return value as ManifestChapter;
}

function makeChapter(value: ManifestChapter, index: number): Chapter {
  const markdown = markdownBySlug[value.slug];
  if (!markdown) {
    throw new Error(`Missing Markdown content for chapter: ${value.slug}`);
  }
  const content = expandEvidenceMarkers(markdown, {
    sourceBaselines: sourceBaselines as SourceBaselines,
    competitorEvidence: competitorEvidence as CompetitorEvidence[],
  });
  const tableOfContents = extractMarkdownHeadings(content)
    .filter(({ level }) => level === 2 || level === 3)
    .map(({ id, title, level, line }) => ({ id, title, level: level as 2 | 3, line }));

  return {
    slug: value.slug,
    index: index + 1,
    title: value.title,
    shortTitle: value.title,
    summary: value.summary,
    group: value.group,
    icon: value.icon,
    accent: value.accent,
    implementationStatus: value.implementation_status,
    evidenceLevel: value.evidence_level,
    content,
    tableOfContents,
    readingMinutes: readingTimeMinutes(content),
  };
}

export const chapters = chapterManifest.chapters.map((chapter, index) => (
  makeChapter(manifestChapter(chapter), index)
));

export const chapterNavigation: ChapterNavigationItem[] = chapters.map(({
  slug,
  index,
  shortTitle,
  summary,
  group,
  icon,
  accent,
}) => ({ slug, index, shortTitle, summary, group, icon, accent }));

export const searchIndex: SearchEntry[] = buildSearchEntries(
  chapters.map(({ slug, title, summary, content }) => ({ slug, title, summary, content })),
);
export const searchIndexVersion = contentVersion(JSON.stringify(searchIndex));

export function getChapter(slug: string): Chapter | undefined {
  return chapters.find((chapter) => chapter.slug === slug);
}
