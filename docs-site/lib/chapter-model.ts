export const chapterGroups = ["理解方案", "运行时", "控制面", "交付"] as const;

export type ChapterGroup = (typeof chapterGroups)[number];

export interface ChapterNavigationItem {
  slug: string;
  index: number;
  shortTitle: string;
  summary: string;
  group: ChapterGroup;
  icon: string;
  accent: string;
}
