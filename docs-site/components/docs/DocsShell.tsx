"use client";

import Link from "next/link";
import {
  ArrowLeft,
  ArrowRight,
  Clock3,
  Menu,
  Moon,
  PanelLeft,
  Sun,
} from "lucide-react";
import { useCallback, useEffect, useRef, useState, useSyncExternalStore } from "react";
import type { Chapter } from "@/lib/chapters";
import type { ChapterNavigationItem } from "@/lib/chapter-model";
import { ChapterIcon } from "./ChapterIcon";
import { MarkdownArticle } from "./MarkdownArticle";
import { MobileTableOfContents } from "./MobileTableOfContents";
import { RouteTraceExplorer } from "./RouteTraceExplorer";
import { Sidebar } from "./Sidebar";

interface DocsShellProps {
  currentChapter: Chapter;
  chapters: ChapterNavigationItem[];
  searchIndexVersion: string;
}

type Theme = "light" | "dark";
const themeChangeEvent = "mesotes-docs-theme-change";
const themeStorageKey = "llm-proxy-docs-theme";
const darkSchemeQuery = "(prefers-color-scheme: dark)";

function systemTheme(): Theme {
  return window.matchMedia(darkSchemeQuery).matches ? "dark" : "light";
}

function preferredTheme(): Theme {
  try {
    const saved = window.localStorage.getItem(themeStorageKey);
    return saved === "dark" || saved === "light" ? saved : systemTheme();
  } catch {
    return systemTheme();
  }
}

function subscribeTheme(listener: () => void) {
  const media = window.matchMedia(darkSchemeQuery);
  const syncTheme = () => {
    document.documentElement.dataset.docsTheme = preferredTheme();
    listener();
  };
  const syncStoredTheme = (event: StorageEvent) => {
    if (event.key === themeStorageKey || event.key === null) {
      syncTheme();
    }
  };
  window.addEventListener(themeChangeEvent, listener);
  window.addEventListener("storage", syncStoredTheme);
  media.addEventListener("change", syncTheme);
  return () => {
    window.removeEventListener(themeChangeEvent, listener);
    window.removeEventListener("storage", syncStoredTheme);
    media.removeEventListener("change", syncTheme);
  };
}

function getThemeSnapshot(): Theme {
  return document.documentElement.dataset.docsTheme === "dark" ? "dark" : "light";
}

function getServerThemeSnapshot(): Theme {
  return "light";
}

function implementationBadgeClass(chapter: Chapter): string {
  if (chapter.implementationStatus === "已实现") {
    return "status-badge--implemented";
  }
  if (chapter.implementationStatus === "后续方向") {
    return "status-badge--future";
  }
  return "status-badge--target";
}

export function DocsShell({ currentChapter, chapters, searchIndexVersion }: DocsShellProps) {
  const [query, setQuery] = useState("");
  const [sidebarOpen, setSidebarOpen] = useState(false);
  const theme = useSyncExternalStore(subscribeTheme, getThemeSnapshot, getServerThemeSnapshot);
  const [progress, setProgress] = useState(0);
  const [activeHeading, setActiveHeading] = useState(
    currentChapter.tableOfContents[0]?.id ?? "",
  );
  const [isNarrow, setIsNarrow] = useState(false);
  const scrollRef = useRef<HTMLElement>(null);
  const menuButtonRef = useRef<HTMLButtonElement>(null);
  const chapterHeadingRef = useRef<HTMLHeadingElement>(null);
  const previousChapterSlug = useRef(currentChapter.slug);
  const currentIndex = chapters.findIndex(({ slug }) => slug === currentChapter.slug);
  const previousChapter = chapters[currentIndex - 1];
  const nextChapter = chapters[currentIndex + 1];
  const sidebarModalOpen = isNarrow && sidebarOpen;

  const closeSidebar = useCallback(() => {
    setSidebarOpen(false);
    if (isNarrow) {
      window.requestAnimationFrame(() => menuButtonRef.current?.focus());
    }
  }, [isNarrow]);

  const focusCurrentDestination = useCallback((headingId?: string) => {
    setSidebarOpen(false);
    window.requestAnimationFrame(() => {
      if (headingId) {
        const element = document.getElementById(headingId);
        element?.scrollIntoView({ block: "start" });
        element?.focus({ preventScroll: true });
        setActiveHeading(headingId);
        return;
      }
      scrollRef.current?.scrollTo({ top: 0, behavior: "auto" });
      chapterHeadingRef.current?.focus({ preventScroll: true });
      setActiveHeading(currentChapter.tableOfContents[0]?.id ?? "");
    });
  }, [currentChapter]);

  useEffect(() => {
    const media = window.matchMedia("(max-width: 900px)");
    const update = () => {
      if (media.matches && !sidebarOpen) {
        const sidebar = document.getElementById("docs-sidebar");
        if (sidebar?.contains(document.activeElement)) {
          menuButtonRef.current?.focus();
        }
      }
      setIsNarrow(media.matches);
      if (!media.matches) {
        setSidebarOpen(false);
      }
    };
    const timer = window.setTimeout(update, 0);
    media.addEventListener("change", update);
    return () => {
      window.clearTimeout(timer);
      media.removeEventListener("change", update);
    };
  }, [sidebarOpen]);

  useEffect(() => {
    const chapterChanged = previousChapterSlug.current !== currentChapter.slug;
    if (chapterChanged) {
      previousChapterSlug.current = currentChapter.slug;
    }
    let frame = 0;
    const syncHash = () => {
      window.cancelAnimationFrame(frame);
      frame = window.requestAnimationFrame(() => {
        const hash = window.location.hash;
        const encodedFragment = hash.slice(1);
        let fragment: string | undefined;
        try {
          fragment = encodedFragment ? decodeURIComponent(encodedFragment) : undefined;
        } catch {
          fragment = undefined;
        }
        const heading = fragment
          ? currentChapter.tableOfContents.find(({ id }) => id === fragment)
          : undefined;
        setActiveHeading(heading?.id ?? currentChapter.tableOfContents[0]?.id ?? "");
        setProgress(0);
        if (heading) {
          const element = document.getElementById(heading.id);
          element?.scrollIntoView({ block: "start" });
          element?.focus({ preventScroll: true });
          return;
        }
        if (!hash) {
          scrollRef.current?.scrollTo({ top: 0, behavior: "auto" });
        }
        if (chapterChanged) {
          chapterHeadingRef.current?.focus({ preventScroll: true });
        }
      });
    };
    syncHash();
    window.addEventListener("hashchange", syncHash);
    return () => {
      window.cancelAnimationFrame(frame);
      window.removeEventListener("hashchange", syncHash);
    };
  }, [currentChapter]);

  useEffect(() => {
    const root = scrollRef.current;
    if (!root) {
      return;
    }
    const targets = currentChapter.tableOfContents
      .map(({ id }) => document.getElementById(id))
      .filter((item): item is HTMLElement => Boolean(item));
    const observer = new IntersectionObserver(
      (entries) => {
        const visible = entries
          .filter((entry) => entry.isIntersecting)
          .sort((left, right) => left.boundingClientRect.top - right.boundingClientRect.top)[0];
        if (visible) {
          setActiveHeading(visible.target.id);
        }
      },
      { root, rootMargin: "-12% 0px -74% 0px", threshold: 0 },
    );
    targets.forEach((target) => observer.observe(target));
    return () => observer.disconnect();
  }, [currentChapter]);

  useEffect(() => {
    function handleKeyboard(event: KeyboardEvent) {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        if (isNarrow) {
          setSidebarOpen(true);
        } else {
          document.getElementById("docs-search")?.focus();
        }
      }
    }
    window.addEventListener("keydown", handleKeyboard);
    return () => window.removeEventListener("keydown", handleKeyboard);
  }, [isNarrow]);

  useEffect(() => {
    if (!isNarrow || !sidebarOpen) {
      return;
    }
    const drawer = document.getElementById("docs-sidebar");
    const search = document.getElementById("docs-search");
    search?.focus();

    function trapFocus(event: KeyboardEvent) {
      if (event.key === "Escape") {
        event.preventDefault();
        closeSidebar();
        return;
      }
      if (event.key !== "Tab" || !drawer) {
        return;
      }
      const focusable = [...drawer.querySelectorAll<HTMLElement>(
        "a[href], button:not([disabled]), input:not([disabled]), [tabindex]:not([tabindex='-1'])",
      )].filter((element) => !element.hidden);
      if (focusable.length === 0) {
        return;
      }
      const first = focusable[0];
      const last = focusable.at(-1)!;
      if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    }

    window.addEventListener("keydown", trapFocus);
    return () => window.removeEventListener("keydown", trapFocus);
  }, [closeSidebar, isNarrow, sidebarOpen]);

  function toggleTheme() {
    const nextTheme: Theme = theme === "light" ? "dark" : "light";
    document.documentElement.dataset.docsTheme = nextTheme;
    try {
      window.localStorage.setItem(themeStorageKey, nextTheme);
    } catch {
      // The active document still adopts the requested theme when storage is unavailable.
    } finally {
      window.dispatchEvent(new Event(themeChangeEvent));
    }
  }

  function updateProgress() {
    const element = scrollRef.current;
    if (!element) {
      return;
    }
    const maximum = element.scrollHeight - element.clientHeight;
    setProgress(maximum <= 0 ? 100 : Math.min(100, (element.scrollTop / maximum) * 100));
  }

  return (
    <div className="docs-app" data-theme={theme}>
      <div className="ambient ambient-one" />
      <div className="ambient ambient-two" />
      <div className="mac-window">
        <header
          className="mac-titlebar"
          aria-hidden={sidebarModalOpen || undefined}
          inert={sidebarModalOpen}
        >
          <div className="traffic-lights" aria-hidden="true">
            <span className="traffic-red" />
            <span className="traffic-yellow" />
            <span className="traffic-green" />
          </div>
          <button
            ref={menuButtonRef}
            className="icon-button mobile-menu"
            onClick={() => setSidebarOpen(true)}
            aria-label="打开章节导航"
            aria-expanded={isNarrow && sidebarOpen}
            aria-controls="docs-sidebar"
          >
            <Menu />
          </button>
          <div className="titlebar-path">
            <PanelLeft aria-hidden="true" />
            <span>Mesotes</span>
            <i>/</i>
            <strong>{currentChapter.shortTitle}</strong>
          </div>
          <div className="titlebar-actions">
            <span className="baseline-pill">
              <i />
              基线：固定快照 · ea13e52
            </span>
            <button className="icon-button" onClick={toggleTheme} aria-label="切换明暗主题">
              {theme === "light" ? <Moon /> : <Sun />}
            </button>
          </div>
        </header>

        <div className="reading-progress" aria-hidden="true">
          <span style={{ width: `${progress}%` }} />
        </div>

        <div className="docs-frame">
          <Sidebar
            chapters={chapters}
            currentSlug={currentChapter.slug}
            searchIndexVersion={searchIndexVersion}
            query={query}
            open={sidebarOpen}
            isNarrow={isNarrow}
            onQueryChange={setQuery}
            onClose={closeSidebar}
            onNavigate={() => setSidebarOpen(false)}
            onCurrentDestinationNavigate={focusCurrentDestination}
          />
          {sidebarModalOpen && (
            <div
              className="sidebar-backdrop"
              onClick={closeSidebar}
              aria-hidden="true"
            />
          )}

          <main
            className="docs-scroll-region"
            ref={scrollRef}
            onScroll={updateProgress}
            aria-hidden={sidebarModalOpen || undefined}
            inert={sidebarModalOpen}
          >
            <div className="document-layout">
              <div className="article-column">
                <section className={`chapter-hero chapter-hero--${currentChapter.accent}`}>
                  <div className="chapter-kicker">
                    <span>Chapter {String(currentChapter.index).padStart(2, "0")}</span>
                    <span className={`status-badge ${implementationBadgeClass(currentChapter)}`}>
                      {currentChapter.implementationStatus}
                    </span>
                    <span className="status-badge status-badge--evidence">
                      {currentChapter.evidenceLevel}
                    </span>
                  </div>
                  <div className="chapter-heading">
                    <ChapterIcon
                      name={currentChapter.icon}
                      accent={currentChapter.accent}
                      size="large"
                    />
                    <div>
                      <h1 ref={chapterHeadingRef} tabIndex={-1}>{currentChapter.title}</h1>
                      <p>{currentChapter.summary}</p>
                    </div>
                  </div>
                  <div className="chapter-meta">
                    <span><Clock3 />约 {currentChapter.readingMinutes} 分钟</span>
                    <span>固定基线 · 2026-07-31</span>
                    <span>范围 · {currentChapter.group}</span>
                  </div>
                </section>

                <MobileTableOfContents items={currentChapter.tableOfContents} />
                <MarkdownArticle content={currentChapter.content} headings={currentChapter.tableOfContents} />
                {currentChapter.slug === "online-routing" && <RouteTraceExplorer />}

                <nav className="chapter-pagination" aria-label="章节翻页">
                  {previousChapter ? (
                    <Link href={`/docs/${previousChapter.slug}`}>
                      <ArrowLeft />
                      <span>
                        <small>上一章</small>
                        <strong>{previousChapter.shortTitle}</strong>
                      </span>
                    </Link>
                  ) : <span />}
                  {nextChapter ? (
                    <Link className="next" href={`/docs/${nextChapter.slug}`}>
                      <span>
                        <small>下一章</small>
                        <strong>{nextChapter.shortTitle}</strong>
                      </span>
                      <ArrowRight />
                    </Link>
                  ) : <span />}
                </nav>
              </div>

              <aside className="page-outline" aria-label="本页目录">
                <div className="outline-sticky">
                  <p className="outline-title">本页目录</p>
                  <nav aria-label="本页章节">
                    {currentChapter.tableOfContents.map((item) => (
                      <a
                        key={item.id}
                        className={`${item.level === 3 ? "is-nested" : ""} ${activeHeading === item.id ? "is-active" : ""}`}
                        href={`#${item.id}`}
                      >
                        {item.title}
                      </a>
                    ))}
                  </nav>
                  <div className="scope-card">
                    <span>阅读边界</span>
                    <strong>文档目标 ≠ 运行时已实现</strong>
                    <p>固定源码事实与待验证目标设计分别标注，不能互相替代。</p>
                  </div>
                  <div className="keyboard-hint">
                    <span><kbd>⌘</kbd><kbd>K</kbd></span>
                    <span>搜索</span>
                  </div>
                </div>
              </aside>
            </div>
          </main>
        </div>
      </div>
    </div>
  );
}
