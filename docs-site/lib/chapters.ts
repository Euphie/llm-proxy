import overviewMarkdown from "../content/01-overview.md?raw";
import currentTargetMarkdown from "../content/02-current-vs-target.md?raw";
import profileMarkdown from "../content/03-profile-configuration.md?raw";
import routingMarkdown from "../content/04-online-routing.md?raw";
import qualityMarkdown from "../content/05-quality-and-cost.md?raw";
import reliabilityMarkdown from "../content/06-runtime-reliability.md?raw";
import optimizationMarkdown from "../content/07-dynamic-optimization.md?raw";
import strategyMarkdown from "../content/08-strategy-lifecycle.md?raw";
import engineeringMarkdown from "../content/09-engineering-and-delivery.md?raw";

export type ChapterStatus = "总览" | "现状与目标" | "目标设计" | "工程方案";

export interface TableOfContentsItem {
  id: string;
  title: string;
  level: 2 | 3;
}

export interface Chapter {
  slug: string;
  index: number;
  title: string;
  shortTitle: string;
  summary: string;
  group: "理解方案" | "核心机制" | "工程落地";
  status: ChapterStatus;
  icon: string;
  accent: string;
  content: string;
  tableOfContents: TableOfContentsItem[];
  readingMinutes: number;
  searchText: string;
}

interface ChapterDefinition extends Omit<
  Chapter,
  "index" | "content" | "tableOfContents" | "readingMinutes" | "searchText"
> {
  markdown: string;
}

export function slugifyHeading(value: string): string {
  return value
    .normalize("NFKC")
    .trim()
    .toLowerCase()
    .replace(/[^\p{Letter}\p{Number}]+/gu, "-")
    .replace(/^-+|-+$/g, "");
}

function plainText(value: string): string {
  return value
    .replace(/!\[[^\]]*]\([^)]*\)/g, " ")
    .replace(/\[([^\]]+)]\([^)]*\)/g, "$1")
    .replace(/[`*_>#|]/g, " ")
    .replace(/\s+/g, " ")
    .trim();
}

function extractTableOfContents(markdown: string): TableOfContentsItem[] {
  return markdown
    .split("\n")
    .flatMap((line) => {
      const match = /^(##|###)\s+(.+?)\s*$/.exec(line);
      if (!match) {
        return [];
      }
      const title = plainText(match[2]);
      return [{
        id: slugifyHeading(title),
        title,
        level: match[1].length as 2 | 3,
      }];
    });
}

function readingMinutes(markdown: string): number {
  const characters = plainText(markdown).replace(/\s/g, "").length;
  return Math.max(3, Math.ceil(characters / 520));
}

const definitions: ChapterDefinition[] = [
  {
    slug: "overview",
    title: "产品目标与设计原则",
    shortTitle: "方案总览",
    summary: "先保证任务做对，再把合适的请求交给更经济的执行方案。",
    group: "理解方案",
    status: "总览",
    icon: "compass",
    accent: "blue",
    markdown: overviewMarkdown,
  },
  {
    slug: "current-vs-target",
    title: "当前能力与目标架构",
    shortTitle: "现状与目标",
    summary: "把 main 已有能力、第一版新增能力和未来方向明确分开。",
    group: "理解方案",
    status: "现状与目标",
    icon: "layers",
    accent: "cyan",
    markdown: currentTargetMarkdown,
  },
  {
    slug: "profile-configuration",
    title: "Profile 配置与核心概念",
    shortTitle: "Profile 配置",
    summary: "参与模型、强基线、任务判断和质量评审分别负责什么。",
    group: "理解方案",
    status: "目标设计",
    icon: "sliders",
    accent: "orange",
    markdown: profileMarkdown,
  },
  {
    slug: "online-routing",
    title: "在线请求如何选择执行方案",
    shortTitle: "在线路由",
    summary: "两轮能力过滤、风险判断和质量约束如何形成一次可审计决策。",
    group: "核心机制",
    status: "目标设计",
    icon: "route",
    accent: "green",
    markdown: routingMarkdown,
  },
  {
    slug: "quality-and-cost",
    title: "如何兼顾质量与成本",
    shortTitle: "质量与成本",
    summary: "质量不是全局排名，成本也不只是主模型单价。",
    group: "核心机制",
    status: "目标设计",
    icon: "scale",
    accent: "yellow",
    markdown: qualityMarkdown,
  },
  {
    slug: "runtime-reliability",
    title: "会话、缓存、视觉与容错",
    shortTitle: "运行可靠性",
    summary: "保持会话稳定，同时允许能力升级、重试和安全切换。",
    group: "核心机制",
    status: "目标设计",
    icon: "shield",
    accent: "teal",
    markdown: reliabilityMarkdown,
  },
  {
    slug: "dynamic-optimization",
    title: "动态策略优化如何工作",
    shortTitle: "动态优化",
    summary: "异步收集可靠证据，但绝不让实验阻塞或改写当前用户回复。",
    group: "核心机制",
    status: "目标设计",
    icon: "activity",
    accent: "red",
    markdown: optimizationMarkdown,
  },
  {
    slug: "strategy-lifecycle",
    title: "策略生命周期、灰度与回滚",
    shortTitle: "策略生命周期",
    summary: "样本只能生成候选，策略必须经过评估、灰度和发布。",
    group: "工程落地",
    status: "工程方案",
    icon: "git-branch",
    accent: "indigo",
    markdown: strategyMarkdown,
  },
  {
    slug: "engineering-and-delivery",
    title: "程序架构、数据安全与实施",
    shortTitle: "工程落地",
    summary: "模块边界、存储结构、敏感数据规则和分阶段实施顺序。",
    group: "工程落地",
    status: "工程方案",
    icon: "database",
    accent: "slate",
    markdown: engineeringMarkdown,
  },
];

export const chapters: Chapter[] = definitions.map((definition, index) => {
  const { markdown, ...metadata } = definition;
  return {
    ...metadata,
    index: index + 1,
    content: markdown,
    tableOfContents: extractTableOfContents(markdown),
    readingMinutes: readingMinutes(markdown),
    searchText: plainText(`${metadata.title} ${metadata.summary} ${markdown}`).toLowerCase(),
  };
});
