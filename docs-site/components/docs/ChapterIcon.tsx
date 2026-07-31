import {
  Activity,
  Compass,
  Database,
  GitBranch,
  Layers3,
  Route,
  Scale,
  ShieldCheck,
  SlidersHorizontal,
  type LucideIcon,
} from "lucide-react";

const icons: Record<string, LucideIcon> = {
  activity: Activity,
  compass: Compass,
  database: Database,
  "git-branch": GitBranch,
  layers: Layers3,
  route: Route,
  scale: Scale,
  shield: ShieldCheck,
  sliders: SlidersHorizontal,
};

interface ChapterIconProps {
  name: string;
  accent: string;
  size?: "small" | "large";
}

export function ChapterIcon({
  name,
  accent,
  size = "small",
}: ChapterIconProps) {
  const Icon = icons[name] ?? Compass;
  return (
    <span
      className={`chapter-icon chapter-icon--${accent} chapter-icon--${size}`}
      aria-hidden="true"
    >
      <Icon strokeWidth={1.9} />
    </span>
  );
}
