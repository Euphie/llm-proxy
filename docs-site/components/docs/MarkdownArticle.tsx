"use client";

import {
  Check,
  Copy,
  ExternalLink,
  Maximize2,
  X,
} from "lucide-react";
import {
  Children,
  isValidElement,
  type ReactNode,
  useEffect,
  useState,
} from "react";
import Image from "next/image";
import ReactMarkdown from "react-markdown";
import remarkGfm from "remark-gfm";
import { slugifyHeading } from "@/lib/chapters";

interface MarkdownArticleProps {
  content: string;
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
  const [copied, setCopied] = useState(false);
  const code = nodeText(children).replace(/\n$/, "");

  async function copy() {
    await navigator.clipboard.writeText(code);
    setCopied(true);
    window.setTimeout(() => setCopied(false), 1400);
  }

  return (
    <div className="code-frame">
      <div className="code-toolbar">
        <span>示例</span>
        <button onClick={copy} aria-label="复制代码">
          {copied ? <Check /> : <Copy />}
          {copied ? "已复制" : "复制"}
        </button>
      </div>
      <pre>{children}</pre>
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

  useEffect(() => {
    if (!open) {
      return;
    }
    const close = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        setOpen(false);
      }
    };
    window.addEventListener("keydown", close);
    return () => window.removeEventListener("keydown", close);
  }, [open]);

  return (
    <>
      <figure className="diagram-figure">
        <button onClick={() => setOpen(true)} aria-label={`放大查看：${alt}`}>
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
      {open && (
        <div className="diagram-lightbox" role="dialog" aria-modal="true" aria-label={alt}>
          <button className="lightbox-close" onClick={() => setOpen(false)} aria-label="关闭大图">
            <X />
          </button>
          <div className="lightbox-canvas" onClick={() => setOpen(false)}>
            <Image
              src={src}
              alt={alt}
              width={1600}
              height={900}
              sizes="96vw"
              unoptimized
              onClick={(event) => event.stopPropagation()}
            />
          </div>
        </div>
      )}
    </>
  );
}

function headingText(children: ReactNode): string {
  return nodeText(Children.toArray(children));
}

export function MarkdownArticle({ content }: MarkdownArticleProps) {
  return (
    <article className="markdown-article">
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        components={{
          h1: () => null,
          h2: ({ children }) => {
            const title = headingText(children);
            return <h2 id={slugifyHeading(title)}>{children}</h2>;
          },
          h3: ({ children }) => {
            const title = headingText(children);
            return <h3 id={slugifyHeading(title)}>{children}</h3>;
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
        }}
      >
        {content}
      </ReactMarkdown>
    </article>
  );
}
