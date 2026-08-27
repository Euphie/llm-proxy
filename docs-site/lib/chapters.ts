import adminWorkflowMarkdown from "../content/admin-workflow.md?raw";
import evaluationMarkdown from "../content/evaluation.md?raw";
import observabilityMarkdown from "../content/observability.md?raw";
import onlineRoutingMarkdown from "../content/online-routing.md?raw";
import operationsMarkdown from "../content/operations.md?raw";
import overviewMarkdown from "../content/overview.md?raw";
import profilesModelsMarkdown from "../content/profiles-models.md?raw";
import reliabilityMarkdown from "../content/reliability.md?raw";
import routingPolicyMarkdown from "../content/routing-policy.md?raw";
import visionMarkdown from "../content/vision.md?raw";
import chapterManifest from "../data/chapters.json";
import {
  isEvidenceLevel,
  type EvidenceLevel,
  type ImplementationStatus,
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
  "admin-workflow": adminWorkflowMarkdown,
  "profiles-models": profilesModelsMarkdown,
  "routing-policy": routingPolicyMarkdown,
  "online-routing": onlineRoutingMarkdown,
  vision: visionMarkdown,
  reliability: reliabilityMarkdown,
  evaluation: evaluationMarkdown,
  observability: observabilityMarkdown,
  operations: operationsMarkdown,
};

function manifestChapter(value: (typeof chapterManifest.chapters)[number]): ManifestChapter {
  if (
    !chapterGroups.includes(value.group as ChapterGroup) ||
    value.implementation_status !== "已实现" ||
    !isEvidenceLevel(value.evidence_level)
  ) {
    throw new Error(`Invalid chapter manifest entry: ${value.slug}`);
  }
  return value as ManifestChapter;
}

function makeChapter(value: ManifestChapter, index: number): Chapter {
  const content = markdownBySlug[value.slug];
  if (!content) throw new Error(`Missing Markdown content for chapter: ${value.slug}`);
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
