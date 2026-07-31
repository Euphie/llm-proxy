"use client";

import Image from "next/image";
import { BookOpen, Command, Search, X } from "lucide-react";
import type { Chapter } from "@/lib/chapters";
import { ChapterIcon } from "./ChapterIcon";

interface SidebarProps {
  chapters: Chapter[];
  activeSlug: string;
  query: string;
  open: boolean;
  onQueryChange: (value: string) => void;
  onSelect: (slug: string) => void;
  onClose: () => void;
}

const groups: Chapter["group"][] = ["理解方案", "核心机制", "工程落地"];

export function Sidebar({
  chapters,
  activeSlug,
  query,
  open,
  onQueryChange,
  onSelect,
  onClose,
}: SidebarProps) {
  const normalized = query.trim().toLowerCase();
  const visibleChapters = normalized
    ? chapters.filter((chapter) => chapter.searchText.includes(normalized))
    : chapters;

  return (
    <aside className={`docs-sidebar ${open ? "is-open" : ""}`} aria-label="文档章节">
      <div className="sidebar-brand">
        <div className="brand-mark" aria-hidden="true">
          <Image src="/brand/mesotes-mark-256.png" alt="" width={38} height={38} />
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
          value={query}
          onChange={(event) => onQueryChange(event.target.value)}
          placeholder="搜索方案与模块"
          autoComplete="off"
        />
        <span className="search-shortcut" aria-hidden="true">
          <Command />
          K
        </span>
      </label>

      <nav className="sidebar-navigation">
        {groups.map((group) => {
          const items = visibleChapters.filter((chapter) => chapter.group === group);
          if (items.length === 0) {
            return null;
          }
          return (
            <section className="nav-group" key={group}>
              <h2>{group}</h2>
              <ul>
                {items.map((chapter) => (
                  <li key={chapter.slug}>
                    <button
                      className={chapter.slug === activeSlug ? "is-active" : ""}
                      onClick={() => onSelect(chapter.slug)}
                      aria-current={chapter.slug === activeSlug ? "page" : undefined}
                    >
                      <ChapterIcon name={chapter.icon} accent={chapter.accent} />
                      <span>
                        <strong>{chapter.shortTitle}</strong>
                        <small>{chapter.summary}</small>
                      </span>
                      <em>{String(chapter.index).padStart(2, "0")}</em>
                    </button>
                  </li>
                ))}
              </ul>
            </section>
          );
        })}

        {visibleChapters.length === 0 && (
          <div className="search-empty">
            <Search aria-hidden="true" />
            <strong>没有匹配章节</strong>
            <span>换一个关键词试试，例如“视觉”或“灰度”。</span>
          </div>
        )}
      </nav>

      <div className="sidebar-note">
        <BookOpen aria-hidden="true" />
        <div>
          <strong>阅读提示</strong>
          <p>蓝色“当前 main”表示已经存在；绿色“目标设计”表示本方案计划新增。</p>
        </div>
      </div>
    </aside>
  );
}
