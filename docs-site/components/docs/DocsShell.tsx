"use client";

import {
  ArrowLeft,
  ArrowRight,
  Clock3,
  Menu,
  Moon,
  PanelLeft,
  Sun,
} from "lucide-react";
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import type { Chapter } from "@/lib/chapters";
import { ChapterIcon } from "./ChapterIcon";
import { MarkdownArticle } from "./MarkdownArticle";
import { Sidebar } from "./Sidebar";

interface DocsShellProps {
  chapters: Chapter[];
}

type Theme = "light" | "dark";

function chapterFromHash(chapters: Chapter[]): Chapter | undefined {
  if (typeof window === "undefined") {
    return undefined;
  }
  const slug = window.location.hash.replace(/^#\/?/, "").split("/")[0];
  return chapters.find((chapter) => chapter.slug === slug);
}

export function DocsShell({ chapters }: DocsShellProps) {
  const [activeSlug, setActiveSlug] = useState(chapters[0]?.slug ?? "");
  const [query, setQuery] = useState("");
  const [sidebarOpen, setSidebarOpen] = useState(false);
  const [theme, setTheme] = useState<Theme>("light");
  const [progress, setProgress] = useState(0);
  const [activeHeading, setActiveHeading] = useState(
    chapters[0]?.tableOfContents[0]?.id ?? "",
  );
  const scrollRef = useRef<HTMLElement>(null);

  const activeIndex = Math.max(0, chapters.findIndex((chapter) => chapter.slug === activeSlug));
  const activeChapter = chapters[activeIndex] ?? chapters[0];
  const previousChapter = chapters[activeIndex - 1];
  const nextChapter = chapters[activeIndex + 1];

  const chapterLookup = useMemo(
    () => new Set(chapters.map((chapter) => chapter.slug)),
    [chapters],
  );

  useEffect(() => {
    const timer = window.setTimeout(() => {
      const initial = chapterFromHash(chapters);
      if (initial) {
        setActiveSlug(initial.slug);
        setActiveHeading(initial.tableOfContents[0]?.id ?? "");
      }
      const saved = window.localStorage.getItem("llm-proxy-docs-theme");
      if (saved === "dark" || saved === "light") {
        setTheme(saved);
      } else if (window.matchMedia("(prefers-color-scheme: dark)").matches) {
        setTheme("dark");
      }
    }, 0);
    return () => window.clearTimeout(timer);
  }, [chapters]);

  useEffect(() => {
    function handleHashChange() {
      const chapter = chapterFromHash(chapters);
      if (chapter) {
        setActiveSlug(chapter.slug);
        setActiveHeading(chapter.tableOfContents[0]?.id ?? "");
      }
    }
    window.addEventListener("hashchange", handleHashChange);
    return () => window.removeEventListener("hashchange", handleHashChange);
  }, [chapters]);

  useEffect(() => {
    const root = scrollRef.current;
    if (!root) {
      return;
    }
    const targets = activeChapter.tableOfContents
      .map((item) => document.getElementById(item.id))
      .filter((item): item is HTMLElement => Boolean(item));
    const observer = new IntersectionObserver(
      (entries) => {
        const visible = entries
          .filter((entry) => entry.isIntersecting)
          .sort((a, b) => a.boundingClientRect.top - b.boundingClientRect.top)[0];
        if (visible) {
          setActiveHeading(visible.target.id);
        }
      },
      { root, rootMargin: "-12% 0px -74% 0px", threshold: 0 },
    );
    targets.forEach((target) => observer.observe(target));
    return () => observer.disconnect();
  }, [activeChapter]);

  const selectChapter = useCallback((slug: string) => {
    if (!chapterLookup.has(slug)) {
      return;
    }
    const chapter = chapters.find((item) => item.slug === slug);
    setActiveSlug(slug);
    setActiveHeading(chapter?.tableOfContents[0]?.id ?? "");
    setSidebarOpen(false);
    window.history.pushState(null, "", `#${slug}`);
    scrollRef.current?.scrollTo({ top: 0, behavior: "auto" });
    setProgress(0);
  }, [chapterLookup, chapters]);

  useEffect(() => {
    function handleKeyboard(event: KeyboardEvent) {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        document.getElementById("docs-search")?.focus();
      }
      if (event.altKey && event.key === "ArrowLeft" && previousChapter) {
        selectChapter(previousChapter.slug);
      }
      if (event.altKey && event.key === "ArrowRight" && nextChapter) {
        selectChapter(nextChapter.slug);
      }
    }
    window.addEventListener("keydown", handleKeyboard);
    return () => window.removeEventListener("keydown", handleKeyboard);
  }, [nextChapter, previousChapter, selectChapter]);

  function toggleTheme() {
    const nextTheme: Theme = theme === "light" ? "dark" : "light";
    setTheme(nextTheme);
    window.localStorage.setItem("llm-proxy-docs-theme", nextTheme);
  }

  function updateProgress() {
    const element = scrollRef.current;
    if (!element) {
      return;
    }
    const maximum = element.scrollHeight - element.clientHeight;
    setProgress(maximum <= 0 ? 100 : Math.min(100, (element.scrollTop / maximum) * 100));
  }

  function scrollToHeading(id: string) {
    document.getElementById(id)?.scrollIntoView({ behavior: "smooth", block: "start" });
  }

  if (!activeChapter) {
    return null;
  }

  return (
    <div className="docs-app" data-theme={theme}>
      <div className="ambient ambient-one" />
      <div className="ambient ambient-two" />
      <div className="mac-window">
        <header className="mac-titlebar">
          <div className="traffic-lights" aria-hidden="true">
            <span className="traffic-red" />
            <span className="traffic-yellow" />
            <span className="traffic-green" />
          </div>
          <button
            className="icon-button mobile-menu"
            onClick={() => setSidebarOpen(true)}
            aria-label="打开章节导航"
          >
            <Menu />
          </button>
          <div className="titlebar-path">
            <PanelLeft aria-hidden="true" />
            <span>llm-proxy</span>
            <i>/</i>
            <strong>{activeChapter.shortTitle}</strong>
          </div>
          <div className="titlebar-actions">
            <span className="baseline-pill">
              <i />
              基线：main · ea13e52
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
            activeSlug={activeChapter.slug}
            query={query}
            open={sidebarOpen}
            onQueryChange={setQuery}
            onSelect={selectChapter}
            onClose={() => setSidebarOpen(false)}
          />
          {sidebarOpen && (
            <button
              className="sidebar-backdrop"
              onClick={() => setSidebarOpen(false)}
              aria-label="关闭章节导航"
            />
          )}

          <main className="docs-scroll-region" ref={scrollRef} onScroll={updateProgress}>
            <div className="document-layout">
              <div className="article-column">
                <section className={`chapter-hero chapter-hero--${activeChapter.accent}`}>
                  <div className="chapter-kicker">
                    <span>Chapter {String(activeChapter.index).padStart(2, "0")}</span>
                    <span className={`status-badge status-badge--${activeChapter.status === "现状与目标" ? "mixed" : activeChapter.status === "总览" ? "overview" : "target"}`}>
                      {activeChapter.status}
                    </span>
                  </div>
                  <div className="chapter-heading">
                    <ChapterIcon
                      name={activeChapter.icon}
                      accent={activeChapter.accent}
                      size="large"
                    />
                    <div>
                      <h1>{activeChapter.title}</h1>
                      <p>{activeChapter.summary}</p>
                    </div>
                  </div>
                  <div className="chapter-meta">
                    <span><Clock3 />约 {activeChapter.readingMinutes} 分钟</span>
                    <span>设计日期 · 2026-07-31</span>
                    <span>适用 · `model=auto`</span>
                  </div>
                </section>

                <MarkdownArticle key={activeChapter.slug} content={activeChapter.content} />

                <nav className="chapter-pagination" aria-label="章节翻页">
                  {previousChapter ? (
                    <button onClick={() => selectChapter(previousChapter.slug)}>
                      <ArrowLeft />
                      <span>
                        <small>上一章</small>
                        <strong>{previousChapter.shortTitle}</strong>
                      </span>
                    </button>
                  ) : <span />}
                  {nextChapter ? (
                    <button className="next" onClick={() => selectChapter(nextChapter.slug)}>
                      <span>
                        <small>下一章</small>
                        <strong>{nextChapter.shortTitle}</strong>
                      </span>
                      <ArrowRight />
                    </button>
                  ) : <span />}
                </nav>
              </div>

              <aside className="page-outline" aria-label="本页目录">
                <div className="outline-sticky">
                  <p className="outline-title">本页目录</p>
                  <nav>
                    {activeChapter.tableOfContents.map((item) => (
                      <button
                        key={item.id}
                        className={`${item.level === 3 ? "is-nested" : ""} ${activeHeading === item.id ? "is-active" : ""}`}
                        onClick={() => scrollToHeading(item.id)}
                      >
                        {item.title}
                      </button>
                    ))}
                  </nav>
                  <div className="scope-card">
                    <span>阅读边界</span>
                    <strong>目标设计 ≠ 已经实现</strong>
                    <p>除明确标注“当前 main”的内容外，其余均为待实施方案。</p>
                  </div>
                  <div className="keyboard-hint">
                    <span><kbd>⌥</kbd><kbd>←</kbd></span>
                    <span>切换章节</span>
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
