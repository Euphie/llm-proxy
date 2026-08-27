import type { Metadata } from "next";
import "./globals.css";

const title = "Mesotes · 智能路由文档";
const description =
  "Mesotes 当前 V2 后台操作、在线链路、评测、排障与上线边界。";
const themeBootstrap = `(() => {
  let theme = "light";
  try {
    const saved = window.localStorage.getItem("llm-proxy-docs-theme");
    theme = saved === "dark" || saved === "light"
      ? saved
      : window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
  } catch {
    theme = window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
  }
  document.documentElement.dataset.docsTheme = theme;
})();`;

export const metadata: Metadata = {
  title,
  description,
  icons: {
    icon: "/favicon.svg",
    shortcut: "/favicon.svg",
    apple: "/brand/mesotes-mark-256.png",
  },
  openGraph: {
    title,
    description,
    type: "website",
  },
  twitter: {
    card: "summary",
    title,
    description,
  },
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="zh-CN" suppressHydrationWarning>
      <head>
        <script id="docs-theme-bootstrap" dangerouslySetInnerHTML={{ __html: themeBootstrap }} />
      </head>
      <body>{children}</body>
    </html>
  );
}
