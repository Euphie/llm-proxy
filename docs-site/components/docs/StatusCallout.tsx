import type { ReactNode } from "react";

export const STATUS_CALLOUTS = {
  CURRENT: "当前已实现",
  EVIDENCE: "证据边界",
} as const;

export type StatusCalloutKind = keyof typeof STATUS_CALLOUTS;

interface StatusCalloutProps {
  kind: StatusCalloutKind;
  children: ReactNode;
}

export function StatusCallout({ kind, children }: StatusCalloutProps) {
  const label = STATUS_CALLOUTS[kind];

  return (
    <aside className={`status-callout status-callout--${kind.toLowerCase()}`} role="note" aria-label={label}>
      <p className="status-callout-label">{label}</p>
      {children}
    </aside>
  );
}
