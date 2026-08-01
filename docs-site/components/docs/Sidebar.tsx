"use client";

import Image from "next/image";
import Link from "next/link";
import { BookOpen, Command, Search, X } from "lucide-react";
import { useEffect, useMemo, useRef, useState, type MouseEvent } from "react";
import { chapterGroups, type ChapterNavigationItem } from "@/lib/chapter-model";
import {
  findSearchMatchRanges,
  normalizeSearchQuery,
  searchEntries,
  type SearchEntry,
  type SearchResult,
} from "@/lib/search";
import { ChapterIcon } from "./ChapterIcon";

interface SidebarProps {
  chapters: ChapterNavigationItem[];
  currentSlug: string;
  searchIndexVersion: string;
  query: string;
  open: boolean;
  isNarrow: boolean;
  onQueryChange: (value: string) => void;
  onClose: () => void;
  onNavigate: () => void;
  onCurrentDestinationNavigate: (headingId?: string) => void;
}

class SearchIndexOutdatedError extends Error {}

function isPlainNavigation(event: MouseEvent<HTMLAnchorElement>) {
  const target = event.currentTarget.target;
  return !event.defaultPrevented &&
    event.button === 0 &&
    !event.metaKey &&
    !event.ctrlKey &&
    !event.shiftKey &&
    !event.altKey &&
    (!target || target === "_self") &&
    !event.currentTarget.hasAttribute("download");
}

function highlight(value: string, query: string) {
  const ranges = findSearchMatchRanges(value, query);
  if (ranges.length === 0) {
    return value;
  }
  let cursor = 0;
  return ranges.flatMap((range, index) => {
    const before = value.slice(cursor, range.start);
    const match = value.slice(range.start, range.end);
    cursor = range.end;
    return [before, <mark key={`${range.start}-${index}`}>{match}</mark>];
  }).concat(value.slice(cursor));
}

function SearchResultLink({
  result,
  query,
  currentSlug,
  onNavigate,
  onCurrentDestinationNavigate,
}: {
  result: SearchResult;
  query: string;
  currentSlug: string;
  onNavigate: () => void;
  onCurrentDestinationNavigate: (headingId?: string) => void;
}) {
  function navigate(event: MouseEvent<HTMLAnchorElement>) {
    if (!isPlainNavigation(event)) return;
    if (result.chapterSlug === currentSlug) {
      onCurrentDestinationNavigate(result.headingId);
    } else {
      onNavigate();
    }
  }

  return (
    <li>
      <Link href={result.href} className="search-result" onClick={navigate}>
        <span>
          <strong>{highlight(result.title, query)}</strong>
          <small>{highlight(result.snippet, query)}</small>
        </span>
        <em>{result.headingId ? "章节" : "页面"}</em>
      </Link>
    </li>
  );
}

function isSearchIndex(value: unknown): value is SearchEntry[] {
  return Array.isArray(value) && value.every((candidate) => {
    if (!candidate || typeof candidate !== "object") {
      return false;
    }
    const entry = candidate as Record<string, unknown>;
    return ["id", "chapterSlug", "chapterTitle", "title", "href", "text", "normalizedText"]
      .every((key) => typeof entry[key] === "string") &&
      typeof entry.order === "number" &&
      (entry.headingId === undefined || typeof entry.headingId === "string");
  });
}

export function Sidebar({
  chapters,
  currentSlug,
  searchIndexVersion,
  query,
  open,
  isNarrow,
  onQueryChange,
  onClose,
  onNavigate,
  onCurrentDestinationNavigate,
}: SidebarProps) {
  const [searchIndex, setSearchIndex] = useState<SearchEntry[] | null>(null);
  const [searchState, setSearchState] = useState<
    "idle" | "loading" | "ready" | "error" | "outdated"
  >("idle");
  const searchRequest = useRef<AbortController | null>(null);
  const normalizedQuery = normalizeSearchQuery(query);
  const results = useMemo(
    () => searchEntries(searchIndex ?? [], query),
    [query, searchIndex],
  );
  const chapterBySlug = useMemo(
    () => new Map(chapters.map((chapter) => [chapter.slug, chapter])),
    [chapters],
  );
  const closedNarrowDrawer = isNarrow && !open;

  useEffect(() => () => {
    searchRequest.current?.abort();
    searchRequest.current = null;
  }, []);

  function loadSearchIndex() {
    if (searchIndex || searchRequest.current || searchState === "outdated") {
      return;
    }
    const controller = new AbortController();
    searchRequest.current = controller;
    setSearchState("loading");
    fetch(`/api/search-index?v=${encodeURIComponent(searchIndexVersion)}`, {
      headers: { accept: "application/json" },
      signal: controller.signal,
    })
      .then((response) => {
        if (response.status === 409) {
          throw new SearchIndexOutdatedError("The document search index has changed.");
        }
        if (!response.ok) {
          throw new Error(`Search index request failed with ${response.status}.`);
        }
        return response.json() as Promise<unknown>;
      })
      .then((entries) => {
        if (!isSearchIndex(entries)) {
          throw new Error("Search index response has an invalid shape.");
        }
        setSearchIndex(entries);
        setSearchState("ready");
      })
      .catch((error: unknown) => {
        if (error instanceof SearchIndexOutdatedError) {
          setSearchState("outdated");
        } else if (!(error instanceof DOMException && error.name === "AbortError")) {
          setSearchState("error");
        }
      })
      .finally(() => {
        if (searchRequest.current === controller) {
          searchRequest.current = null;
        }
      });
  }

  function updateQuery(value: string) {
    onQueryChange(value);
    if (normalizeSearchQuery(value)) {
      loadSearchIndex();
    }
  }

  return (
    <aside
      id="docs-sidebar"
      className={`docs-sidebar ${open ? "is-open" : ""}`}
      aria-label="文档章节"
      role={isNarrow && open ? "dialog" : undefined}
      aria-modal={isNarrow && open ? true : undefined}
      aria-hidden={closedNarrowDrawer || undefined}
      inert={closedNarrowDrawer}
    >
      <div className="sidebar-brand">
        <div className="brand-mark" aria-hidden="true">
          <Image src="/brand/mesotes-mark-256.png" alt="" width={38} height={38} unoptimized />
        </div>
        <div>
          <strong>MESOTES</strong>
          <span>llm-proxy 智能路由设计</span>
        </div>
        <button className="icon-button sidebar-close" onClick={onClose} aria-label="关闭章节导航">
          <X />
        </button>
      </div>

      <label className="search-field">
        <Search aria-hidden="true" />
        <input
          id="docs-search"
          type="search"
          aria-label="搜索方案与模块"
          value={query}
          onChange={(event) => updateQuery(event.target.value)}
          placeholder="搜索方案与模块"
          autoComplete="off"
        />
        <span className="search-shortcut" aria-hidden="true">
          <Command />
          K
        </span>
      </label>

      <div className="search-status">
        <span role="status" aria-live="polite">
          {normalizedQuery
            ? searchState === "outdated"
              ? "文档已更新，请刷新后搜索"
              : searchState === "error"
              ? "搜索索引加载失败"
              : searchState === "loading" || searchIndex === null
                ? "正在加载搜索索引"
                : results.length === 0
              ? "没有匹配结果"
              : `找到 ${results.length} 条结果`
            : "输入关键词可搜索页面与章节"}
        </span>
        {normalizedQuery && searchState === "error" && (
          <button type="button" onClick={loadSearchIndex}>重试</button>
        )}
        {normalizedQuery && searchState === "outdated" && (
          <button type="button" onClick={() => window.location.reload()}>刷新文档</button>
        )}
      </div>

      <nav className="sidebar-navigation" aria-label="章节导航">
        {normalizedQuery && searchIndex ? (
          chapterGroups.map((group) => {
            const groupResults = results.filter((result) => (
              chapterBySlug.get(result.chapterSlug)?.group === group
            ));
            if (groupResults.length === 0) {
              return null;
            }
            return (
              <section className="nav-group search-group" key={group}>
                <h2>{group}</h2>
                <ul>
                  {groupResults.map((result) => (
                    <SearchResultLink
                      key={result.id}
                      result={result}
                      query={query}
                      currentSlug={currentSlug}
                      onNavigate={onNavigate}
                      onCurrentDestinationNavigate={onCurrentDestinationNavigate}
                    />
                  ))}
                </ul>
              </section>
            );
          })
        ) : (
          chapterGroups.map((group) => {
            const items = chapters.filter((chapter) => chapter.group === group);
            return (
              <section className="nav-group" key={group}>
                <h2>{group}</h2>
                <ul>
                  {items.map((chapter) => (
                    <li key={chapter.slug}>
                      <Link
                        href={`/docs/${chapter.slug}`}
                        className={chapter.slug === currentSlug ? "is-active" : ""}
                        aria-current={chapter.slug === currentSlug ? "page" : undefined}
                        onClick={(event) => {
                          if (!isPlainNavigation(event)) return;
                          if (chapter.slug === currentSlug) {
                            onCurrentDestinationNavigate();
                          } else {
                            onNavigate();
                          }
                        }}
                      >
                        <ChapterIcon name={chapter.icon} accent={chapter.accent} />
                        <span>
                          <strong>{chapter.shortTitle}</strong>
                          <small>{chapter.summary}</small>
                        </span>
                        <em>{String(chapter.index).padStart(2, "0")}</em>
                      </Link>
                    </li>
                  ))}
                </ul>
              </section>
            );
          })
        )}

        {normalizedQuery && searchIndex && results.length === 0 && (
          <div className="search-empty">
            <Search aria-hidden="true" />
            <strong>没有匹配章节</strong>
            <span>换一个关键词试试，例如“Profile”或“灰度”。</span>
          </div>
        )}
      </nav>

      <div className="sidebar-note">
        <BookOpen aria-hidden="true" />
        <div>
          <strong>阅读提示</strong>
          <p>状态与证据徽章分别说明目标阶段和当前可追溯的证据来源。</p>
        </div>
      </div>
    </aside>
  );
}
