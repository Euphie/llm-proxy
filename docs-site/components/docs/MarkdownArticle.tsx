"use client";

import {
  Check,
  Copy,
  ExternalLink,
  Link2,
  Maximize2,
  X,
} from "lucide-react";
import {
  Children,
  cloneElement,
  isValidElement,
  type ReactElement,
  type MouseEvent,
  type ReactNode,
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import { createPortal } from "react-dom";
import Image from "next/image";
import ReactMarkdown, { type Components } from "react-markdown";
import remarkGfm from "remark-gfm";
import type { TableOfContentsItem } from "@/lib/chapters";
import { getStatusCalloutMatch } from "@/lib/markdown-directives";
import { slugifyHeading } from "@/lib/search";
import { StatusCallout, type StatusCalloutKind } from "./StatusCallout";

interface MarkdownArticleProps {
  content: string;
  headings: TableOfContentsItem[];
}

function nodeText(node: ReactNode): string {
  if (typeof node === "string" || typeof node === "number") {
    return String(node);
  }
  if (Array.isArray(node)) {
    return node.map(nodeText).join("");
  }
  if (isValidElement<{ children?: ReactNode }>(node)) {
    return nodeText(node.props.children);
  }
  return "";
}

function CodeBlock({ children }: { children?: ReactNode }) {
  const [copyStatus, setCopyStatus] = useState<"idle" | "copied" | "failed">("idle");
  const resetTimer = useRef<number | undefined>(undefined);
  const mounted = useRef(false);
  const code = nodeText(children).replace(/\n$/, "");
  const codeElement = Children.toArray(children).find(
    (child): child is ReactElement<{ className?: string }> => isValidElement(child),
  );
  const language = /(?:^|\s)language-([^\s]+)/.exec(codeElement?.props.className ?? "")?.[1] ?? "text";

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
      window.clearTimeout(resetTimer.current);
    };
  }, []);

  function resetStatus() {
    window.clearTimeout(resetTimer.current);
    resetTimer.current = window.setTimeout(() => setCopyStatus("idle"), 1600);
  }

  async function copy() {
    try {
      if (!navigator.clipboard?.writeText) {
        throw new Error("Clipboard API is unavailable.");
      }
      await navigator.clipboard.writeText(code);
      if (!mounted.current) return;
      setCopyStatus("copied");
    } catch {
      if (!mounted.current) return;
      setCopyStatus("failed");
    }
    resetStatus();
  }

  return (
    <div className="code-frame">
      <div className="code-toolbar">
        <span className="code-language">{language}</span>
        <div className="code-copy-controls">
          <span className="code-copy-status" aria-live="polite" aria-atomic="true">
            {copyStatus === "copied" ? "已复制" : copyStatus === "failed" ? "复制失败" : ""}
          </span>
          <button type="button" onClick={copy} aria-label="复制代码">
            {copyStatus === "copied" ? <Check /> : <Copy />}
            {copyStatus === "copied" ? "已复制" : copyStatus === "failed" ? "复制失败" : "复制"}
          </button>
        </div>
      </div>
      <pre role="group" aria-label={`${language} 代码块`} tabIndex={0}>{children}</pre>
    </div>
  );
}

function Diagram({
  src,
  alt,
}: {
  src: string;
  alt: string;
}) {
  const [open, setOpen] = useState(false);
  const dialogRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const shouldRestoreFocus = useRef(false);
  const setCloseButtonRef = useCallback((button: HTMLButtonElement | null) => {
    button?.focus();
  }, []);
  const closeDialog = useCallback(() => {
    shouldRestoreFocus.current = true;
    setOpen(false);
  }, []);
  const setDialogRef = useCallback((dialog: HTMLDivElement | null) => {
    dialogRef.current = dialog;
  }, []);

  useEffect(() => {
    if (!open) {
      return;
    }
    const app = document.querySelector<HTMLElement>(".docs-app");
    const previousInert = app?.inert ?? false;
    const previousAriaHidden = app?.getAttribute("aria-hidden") ?? null;
    if (app) {
      app.inert = true;
      app.setAttribute("aria-hidden", "true");
    }
    return () => {
      if (app) {
        app.inert = previousInert;
        if (previousAriaHidden === null) {
          app.removeAttribute("aria-hidden");
        } else {
          app.setAttribute("aria-hidden", previousAriaHidden);
        }
      }
      if (shouldRestoreFocus.current) {
        triggerRef.current?.focus();
        shouldRestoreFocus.current = false;
      }
    };
  }, [open]);

  useEffect(() => {
    if (!open) {
      return;
    }

    const focusableSelector = "a[href], button:not([disabled]), input:not([disabled]), [tabindex]:not([tabindex='-1'])";
    const trapFocus = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        closeDialog();
        return;
      }
      if (event.key !== "Tab" || !dialogRef.current) {
        return;
      }
      const focusable = [...dialogRef.current.querySelectorAll<HTMLElement>(focusableSelector)]
        .filter((element) => !element.hidden);
      const first = focusable[0];
      const last = focusable.at(-1);
      if (!first || !last) {
        event.preventDefault();
      } else if (!dialogRef.current.contains(document.activeElement)) {
        event.preventDefault();
        (event.shiftKey ? last : first).focus();
      } else if (event.shiftKey && document.activeElement === first) {
        event.preventDefault();
        last.focus();
      } else if (!event.shiftKey && document.activeElement === last) {
        event.preventDefault();
        first.focus();
      }
    };

    window.addEventListener("keydown", trapFocus);
    return () => {
      window.removeEventListener("keydown", trapFocus);
    };
  }, [closeDialog, open]);

  function openDialog(event: MouseEvent<HTMLButtonElement>) {
    triggerRef.current = event.currentTarget;
    setOpen(true);
  }

  return (
    <>
      <figure className="diagram-figure">
        <button type="button" onClick={openDialog} aria-label={`放大查看：${alt}`}>
          <Image
            src={src}
            alt={alt}
            width={1600}
            height={900}
            sizes="(max-width: 960px) 92vw, 980px"
            unoptimized
          />
          <span>
            <Maximize2 />
            放大查看
          </span>
        </button>
        <figcaption>{alt}</figcaption>
      </figure>
      {open && createPortal((
        <div
          className="diagram-lightbox"
          role="dialog"
          aria-modal="true"
          aria-labelledby="diagram-lightbox-title"
          ref={setDialogRef}
          onMouseDown={(event) => {
            if (event.target === event.currentTarget) {
              closeDialog();
            }
          }}
        >
          <p id="diagram-lightbox-title" className="visually-hidden">{alt}</p>
          <button ref={setCloseButtonRef} className="lightbox-close" type="button" onClick={closeDialog}>
            <X aria-hidden="true" />
            <span>关闭</span>
          </button>
          <div
            className="lightbox-canvas"
            role="region"
            aria-label={`可滚动图表：${alt}`}
            tabIndex={0}
          >
            <Image
              src={src}
              alt={alt}
              width={1600}
              height={900}
              sizes="96vw"
              unoptimized
            />
          </div>
        </div>
      ), document.body)}
    </>
  );
}

function stripStatusCalloutToken(children: ReactNode, strippedText: string): ReactNode {
  const blockquoteChildren = Children.toArray(children);
  const paragraphIndex = blockquoteChildren.findIndex(isValidElement);
  const firstParagraph = blockquoteChildren[paragraphIndex];
  if (!isValidElement<{ children?: ReactNode }>(firstParagraph)) {
    return children;
  }
  const paragraphChildren = Children.toArray(firstParagraph.props.children);
  if (typeof paragraphChildren[0] !== "string") {
    return children;
  }
  return blockquoteChildren.map((child, index) => (
    index === paragraphIndex
      ? cloneElement(firstParagraph, undefined, strippedText, ...paragraphChildren.slice(1))
      : child
  ));
}

function HeadingAnchor({ id, title }: { id: string; title: string }) {
  return (
    <a className="heading-anchor" href={`#${id}`} aria-label={`链接到${title}`}>
      <Link2 aria-hidden="true" />
    </a>
  );
}

function headingText(children: ReactNode): string {
  return nodeText(Children.toArray(children));
}

export function MarkdownArticle({ content, headings }: MarkdownArticleProps) {
  const headingIds = useMemo(
    () => new Map(headings.map(({ line, id }) => [line, id])),
    [headings],
  );
  const components = useMemo<Components>(() => ({
    h1: () => null,
    h2: ({ children, node }) => {
      const title = headingText(children);
      const id = headingIds.get(node?.position?.start.line ?? -1) ?? slugifyHeading(title);
      return <h2 id={id} aria-label={title} tabIndex={-1}>{children}<HeadingAnchor id={id} title={title} /></h2>;
    },
    h3: ({ children, node }) => {
      const title = headingText(children);
      const id = headingIds.get(node?.position?.start.line ?? -1) ?? slugifyHeading(title);
      return <h3 id={id} aria-label={title} tabIndex={-1}>{children}<HeadingAnchor id={id} title={title} /></h3>;
    },
    blockquote: ({ children, node }) => {
      const match = getStatusCalloutMatch(node);
      return match
        ? <StatusCallout kind={match.kind as StatusCalloutKind}>{stripStatusCalloutToken(children, match.strippedText)}</StatusCallout>
        : <blockquote>{children}</blockquote>;
    },
    pre: ({ children }) => <CodeBlock>{children}</CodeBlock>,
    code: ({ className, children }) => (
      <code className={className}>{children}</code>
    ),
    a: ({ href = "", children }) => {
      const external = /^https?:\/\//.test(href);
      return (
        <a href={href} target={external ? "_blank" : undefined} rel={external ? "noreferrer" : undefined}>
          {children}
          {external && <ExternalLink aria-hidden="true" />}
        </a>
      );
    },
    img: ({ src = "", alt = "架构图" }) => (
      <Diagram src={typeof src === "string" ? src : ""} alt={alt} />
    ),
    p: ({ children }) => {
      const items = Children.toArray(children);
      const onlyChild = items[0];
      if (
        items.length === 1 &&
        isValidElement<{ src?: unknown }>(onlyChild) &&
        typeof onlyChild.props.src === "string" &&
        onlyChild.props.src.startsWith("/diagrams/")
      ) {
        return <>{children}</>;
      }
      return <p>{children}</p>;
    },
    table: ({ children }) => (
      <div className="table-scroll">
        <table>{children}</table>
      </div>
    ),
  }), [headingIds]);

  return (
    <article className="markdown-article">
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        components={components}
      >
        {content}
      </ReactMarkdown>
    </article>
  );
}
