import type { Metadata } from "next";
import { headers } from "next/headers";
import "./globals.css";

const title = "llm-proxy 智能路由设计";
const description =
  "从当前 main 基线出发，完整解释 llm-proxy 智能路由、视觉执行方案、动态策略优化与工程落地。";

function firstHeaderValue(value: string | null): string | undefined {
  return value?.split(",")[0]?.trim() || undefined;
}

function configuredOrigin(): string | undefined {
  const configured = process.env.NEXT_PUBLIC_SITE_URL?.trim();
  if (!configured) {
    return undefined;
  }
  try {
    const url = new URL(configured);
    if (
      (url.protocol !== "http:" && url.protocol !== "https:") ||
      url.username ||
      url.password
    ) {
      return undefined;
    }
    return url.origin;
  } catch {
    return undefined;
  }
}

function localPreviewOrigin(host: string | undefined): string | undefined {
  if (
    !host ||
    !/^(?:localhost|127\.0\.0\.1|\[::1\])(?::\d{1,5})?$/.test(host)
  ) {
    return undefined;
  }
  try {
    return new URL(`http://${host}`).origin;
  } catch {
    return undefined;
  }
}

export async function generateMetadata(): Promise<Metadata> {
  const requestHeaders = await headers();
  const origin =
    configuredOrigin() ??
    localPreviewOrigin(firstHeaderValue(requestHeaders.get("host")));
  const image = origin
    ? new URL("/og-routing-design.png", origin)
    : undefined;

  return {
    title: {
      default: title,
      template: "%s · llm-proxy",
    },
    description,
    icons: {
      icon: "/favicon.svg",
      shortcut: "/favicon.svg",
    },
    openGraph: {
      title,
      description,
      type: "website",
      images: image
        ? [{ url: image, width: 1731, height: 909, alt: title }]
        : undefined,
    },
    twitter: {
      card: "summary_large_image",
      title,
      description,
      images: image ? [image] : undefined,
    },
  };
}

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="zh-CN">
      <body>{children}</body>
    </html>
  );
}
