"use client";

import { ChevronDown } from "lucide-react";
import { useState } from "react";
import type { TableOfContentsItem } from "@/lib/chapters";

interface MobileTableOfContentsProps {
  items: TableOfContentsItem[];
}

export function MobileTableOfContents({ items }: MobileTableOfContentsProps) {
  const [open, setOpen] = useState(false);

  if (items.length === 0) {
    return null;
  }

  return (
    <section className="mobile-toc" aria-label="本页目录">
      <button
        type="button"
        aria-expanded={open}
        aria-controls="mobile-page-outline"
        onClick={() => setOpen((value) => !value)}
      >
        <span>本页目录</span>
        <ChevronDown aria-hidden="true" />
      </button>
      <nav id="mobile-page-outline" aria-label="移动端本页章节" hidden={!open}>
        {items.map((item) => (
          <a key={item.id} className={item.level === 3 ? "is-nested" : ""} href={`#${item.id}`}>
            {item.title}
          </a>
        ))}
      </nav>
    </section>
  );
}
