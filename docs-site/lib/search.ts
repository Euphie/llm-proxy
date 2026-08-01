export interface SearchDocument {
  slug: string;
  title: string;
  summary: string;
  content: string;
}

export interface MarkdownHeading {
  id: string;
  title: string;
  level: number;
  line: number;
}

export interface SearchEntry {
  id: string;
  chapterSlug: string;
  chapterTitle: string;
  headingId?: string;
  title: string;
  href: string;
  text: string;
  normalizedText: string;
  order: number;
}

export interface SearchResult extends SearchEntry {
  score: number;
  snippet: string;
}

export interface SearchMatchRange {
  start: number;
  end: number;
}

interface ParsedHeading extends MarkdownHeading {
  rawLine: number;
}

interface FenceState {
  marker: "`" | "~";
  length: number;
}

function readFenceLine(line: string, state: FenceState | null): {
  state: FenceState | null;
  boundary: boolean;
} {
  if (state) {
    const closing = new RegExp(`^ {0,3}${state.marker}{${state.length},}[ \\t]*$`);
    return closing.test(line)
      ? { state: null, boundary: true }
      : { state, boundary: false };
  }

  const opening = /^ {0,3}(`{3,}|~{3,})(.*)$/.exec(line);
  if (!opening || (opening[1][0] === "`" && opening[2].includes("`"))) {
    return { state: null, boundary: false };
  }
  return {
    state: { marker: opening[1][0] as "`" | "~", length: opening[1].length },
    boundary: true,
  };
}

export function normalizeSearchQuery(value: string): string {
  return value.normalize("NFKC").toLocaleLowerCase().replace(/\s+/g, " ").trim();
}

export function contentVersion(value: string): string {
  let primary = 0x811c9dc5;
  let secondary = 0x9e3779b9;
  for (let index = 0; index < value.length; index += 1) {
    const code = value.charCodeAt(index);
    primary = Math.imul(primary ^ code, 0x01000193);
    secondary = Math.imul(secondary ^ code, 0x85ebca6b);
  }
  return [primary, secondary]
    .map((hash) => (hash >>> 0).toString(16).padStart(8, "0"))
    .join("");
}

export function slugifyHeading(value: string): string {
  return normalizeSearchQuery(value)
    .replace(/[^\p{Letter}\p{Number}]+/gu, "-")
    .replace(/^-+|-+$/g, "");
}

export function markdownToPlainText(markdown: string): string {
  let fenceState: FenceState | null = null;
  const lines: string[] = [];

  for (const line of markdown.replace(/^---\s*\n[\s\S]*?\n---\s*(?:\n|$)/, "").split(/\r?\n/)) {
    const fence = readFenceLine(line, fenceState);
    fenceState = fence.state;
    if (fence.boundary) continue;
    lines.push(line);
  }

  return lines.join("\n")
    .replace(/<!--[^]*?-->/g, " ")
    .replace(/!\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/\[([^\]]+)\]\([^)]*\)/g, "$1")
    .replace(/<https?:\/\/[^>]+>/g, " ")
    .replace(/<[^>]+>/g, " ")
    .replace(/`+([^`]+)`+/g, "$1")
    .replace(/^\s{0,3}(?:#{1,6}\s*|>\s*|[-+*]\s+|\d+[.)]\s+)/gm, "")
    .replace(/^\s*\|?|\|?\s*$/gm, " ")
    .replace(/[|*~]/g, " ")
    .replace(/\s+/g, " ")
    .trim();
}

function parseHeadings(markdown: string): ParsedHeading[] {
  const headings: ParsedHeading[] = [];
  const used = new Set<string>();
  const nextSuffix = new Map<string, number>();
  let fenceState: FenceState | null = null;

  markdown.split(/\r?\n/).forEach((line, rawLine) => {
    const wasInFence = fenceState !== null;
    const fence = readFenceLine(line, fenceState);
    fenceState = fence.state;
    if (fence.boundary || wasInFence) return;

    const match = /^\s{0,3}(#{1,6})(?:[ \t]+(.*?)\s*|[ \t]*)$/.exec(line);
    if (!match) return;
    const title = markdownToPlainText((match[2] ?? "").replace(/[ \t]+#+[ \t]*$/, ""));
    const base = slugifyHeading(title) || "section";
    let suffix = nextSuffix.get(base) ?? 1;
    let id = suffix === 1 ? base : `${base}-${suffix}`;
    while (used.has(id)) {
      suffix += 1;
      id = `${base}-${suffix}`;
    }
    used.add(id);
    nextSuffix.set(base, suffix + 1);
    headings.push({ id, title, level: match[1].length, line: rawLine + 1, rawLine });
  });
  return headings;
}

export function extractMarkdownHeadings(markdown: string): MarkdownHeading[] {
  return parseHeadings(markdown).map(({ id, title, level, line }) => ({ id, title, level, line }));
}

export function readingTimeMinutes(markdown: string, charactersPerMinute = 520): number {
  if (!Number.isFinite(charactersPerMinute) || charactersPerMinute <= 0) {
    throw new RangeError("charactersPerMinute must be greater than zero.");
  }
  const characters = markdownToPlainText(markdown).replace(/\s/g, "").length;
  return Math.max(1, Math.ceil(characters / charactersPerMinute));
}

export function buildSearchEntries(documents: SearchDocument[]): SearchEntry[] {
  let order = 0;
  return documents.flatMap((document) => {
    const headings = parseHeadings(document.content);
    const lines = document.content.split(/\r?\n/);
    const firstSectionLine = headings.find(({ level }) => level === 2)?.rawLine ?? lines.length;
    const preamble = markdownToPlainText(lines.slice(0, firstSectionLine).join("\n"));
    const rootText = [document.title, document.summary, preamble].filter(Boolean).join(" ");
    const root: SearchEntry = {
      id: document.slug,
      chapterSlug: document.slug,
      chapterTitle: document.title,
      title: document.title,
      href: `/docs/${document.slug}`,
      text: rootText,
      normalizedText: normalizeSearchQuery(rootText),
      order: order++,
    };
    const sections = headings.flatMap((heading, index): SearchEntry[] => {
      if (heading.level === 1) {
        return [];
      }
      const nextLine = headings[index + 1]?.rawLine ?? lines.length;
      const body = markdownToPlainText(lines.slice(heading.rawLine + 1, nextLine).join("\n"));
      const text = [document.title, heading.title, body].filter(Boolean).join(" ");
      return [{
        id: `${document.slug}:${heading.id}`,
        chapterSlug: document.slug,
        chapterTitle: document.title,
        headingId: heading.id,
        title: heading.title || document.title,
        href: `/docs/${document.slug}#${heading.id}`,
        text,
        normalizedText: normalizeSearchQuery(text),
        order: order++,
      }];
    });
    return [root, ...sections];
  });
}

interface NormalizedSourceMap {
  normalized: string;
  starts: number[];
  ends: number[];
}

function normalizeWithSourceMap(value: string): NormalizedSourceMap {
  let normalized = "";
  const starts: number[] = [];
  const ends: number[] = [];
  let pendingWhitespace: { start: number; end: number } | undefined;
  const segments = new Intl.Segmenter(undefined, { granularity: "grapheme" }).segment(value);

  for (const segment of segments) {
    const sourceStart = segment.index;
    const sourceEnd = sourceStart + segment.segment.length;
    const normalizedPart = segment.segment.normalize("NFKC").toLocaleLowerCase();

    for (const character of normalizedPart) {
      if (/\s/u.test(character)) {
        if (normalized) {
          pendingWhitespace ??= { start: sourceStart, end: sourceEnd };
        }
        continue;
      }
      if (pendingWhitespace) {
        normalized += " ";
        starts.push(pendingWhitespace.start);
        ends.push(pendingWhitespace.end);
        pendingWhitespace = undefined;
      }
      normalized += character;
      for (let index = 0; index < character.length; index += 1) {
        starts.push(sourceStart);
        ends.push(sourceEnd);
      }
    }
  }

  return { normalized, starts, ends };
}

function sourceRangeAt(
  sourceMap: NormalizedSourceMap,
  normalizedStart: number,
  normalizedLength: number,
): SearchMatchRange | undefined {
  const start = sourceMap.starts[normalizedStart];
  const end = sourceMap.ends[normalizedStart + normalizedLength - 1];
  return start === undefined || end === undefined ? undefined : { start, end };
}

function matchRanges(sourceMap: NormalizedSourceMap, normalizedQuery: string): SearchMatchRange[] {
  const candidates: SearchMatchRange[] = [];
  const tokens = [...new Set(normalizedQuery.split(" ").filter(Boolean))];

  for (const token of tokens) {
    let from = 0;
    while (from < sourceMap.normalized.length) {
      const index = sourceMap.normalized.indexOf(token, from);
      if (index < 0) {
        break;
      }
      const range = sourceRangeAt(sourceMap, index, token.length);
      if (range) {
        candidates.push(range);
      }
      from = index + token.length;
    }
  }

  return candidates
    .sort((left, right) => left.start - right.start || right.end - left.end)
    .reduce<SearchMatchRange[]>((ranges, range) => {
      if (range.start >= (ranges.at(-1)?.end ?? 0)) {
        ranges.push(range);
      }
      return ranges;
    }, []);
}

export function findSearchMatchRanges(value: string, query: string): SearchMatchRange[] {
  const normalizedQuery = normalizeSearchQuery(query);
  if (!normalizedQuery) {
    return [];
  }
  return matchRanges(normalizeWithSourceMap(value), normalizedQuery);
}

function snippet(text: string, normalizedQuery: string, maximum = 112): string {
  const sourceMap = normalizeWithSourceMap(text);
  const phraseIndex = sourceMap.normalized.indexOf(normalizedQuery);
  const phraseRange = phraseIndex >= 0
    ? sourceRangeAt(sourceMap, phraseIndex, normalizedQuery.length)
    : undefined;
  const [sourceRange = { start: 0, end: 0 }] = phraseRange
    ? [phraseRange]
    : matchRanges(sourceMap, normalizedQuery);
  const sourceHitStart = sourceRange.start;
  const sourceHitEnd = sourceRange.end;
  const start = Math.max(0, sourceHitStart - Math.floor(maximum / 3));
  const end = Math.min(text.length, Math.max(start + maximum, sourceHitEnd));
  return `${start > 0 ? "…" : ""}${text.slice(start, end).trim()}${end < text.length ? "…" : ""}`;
}

export function searchEntries(entries: SearchEntry[], query: string, limit = 20): SearchResult[] {
  const normalizedQuery = normalizeSearchQuery(query);
  if (!normalizedQuery || limit <= 0) return [];
  const tokens = normalizedQuery.split(" ");

  return entries.flatMap((entry) => {
    if (!tokens.every((token) => entry.normalizedText.includes(token))) return [];
    const normalizedTitle = normalizeSearchQuery(entry.title);
    const normalizedChapter = normalizeSearchQuery(entry.chapterTitle);
    let score = normalizedTitle === normalizedQuery ? 1000 : 0;
    if (normalizedTitle.includes(normalizedQuery)) score += 500;
    score += tokens.filter((token) => normalizedTitle.includes(token)).length * 100;
    if (normalizedChapter.includes(normalizedQuery)) score += 60;
    if (entry.normalizedText.includes(normalizedQuery)) score += 30;
    score += tokens.filter((token) => entry.normalizedText.includes(token)).length * 10;
    return [{ ...entry, score, snippet: snippet(entry.text, normalizedQuery) }];
  }).sort((left, right) => right.score - left.score || left.order - right.order)
    .slice(0, Math.floor(limit));
}
