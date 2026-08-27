import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { DocsShell } from "@/components/docs/DocsShell";
import { chapterNavigation, getChapter, searchIndexVersion } from "@/lib/chapters";
import { getSiteOrigin } from "@/lib/site-origin";

interface DocumentPageProps {
  params: Promise<{ slug: string }>;
}

export function generateStaticParams() {
  return chapterNavigation.map(({ slug }) => ({ slug }));
}

export async function generateMetadata({ params }: DocumentPageProps): Promise<Metadata> {
  const { slug } = await params;
  const chapter = getChapter(slug);
  if (!chapter) {
    return {};
  }

  const origin = getSiteOrigin();
  const canonical = new URL(`/docs/${chapter.slug}`, origin).toString();
  const title = `${chapter.title} · Mesotes`;
  return {
    title,
    description: chapter.summary,
    alternates: { canonical },
    openGraph: {
      title,
      description: chapter.summary,
      url: canonical,
      type: "article",
    },
    twitter: {
      card: "summary",
      title,
      description: chapter.summary,
    },
  };
}

export default async function DocumentPage({ params }: DocumentPageProps) {
  const { slug } = await params;
  const chapter = getChapter(slug);
  if (!chapter) {
    notFound();
  }

  return (
    <DocsShell
      currentChapter={chapter}
      chapters={chapterNavigation}
      searchIndexVersion={searchIndexVersion}
    />
  );
}
