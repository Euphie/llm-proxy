import { DocsShell } from "@/components/docs/DocsShell";
import { chapters } from "@/lib/chapters";

export default function Home() {
  return <DocsShell chapters={chapters} />;
}
