export const STATUS_DIRECTIVE_KINDS = ["CURRENT", "EVIDENCE"] as const;

export type StatusDirectiveKind = (typeof STATUS_DIRECTIVE_KINDS)[number];

export interface StatusCalloutMatch {
  kind: StatusDirectiveKind;
  strippedText: string;
}

interface HastText {
  type: "text";
  value: string;
}

interface HastElement {
  type: "element";
  tagName: string;
  children?: unknown[];
}

function isHastElement(value: unknown): value is HastElement {
  return typeof value === "object" && value !== null
    && (value as Partial<HastElement>).type === "element"
    && typeof (value as Partial<HastElement>).tagName === "string";
}

function isHastText(value: unknown): value is HastText {
  return typeof value === "object" && value !== null
    && (value as Partial<HastText>).type === "text"
    && typeof (value as Partial<HastText>).value === "string";
}

export function getStatusCalloutMatch(node: unknown): StatusCalloutMatch | undefined {
  if (!isHastElement(node) || node.tagName !== "blockquote") {
    return undefined;
  }
  const firstParagraph = node.children?.find(
    (child): child is HastElement => isHastElement(child) && child.tagName === "p",
  );
  if (!isHastElement(firstParagraph)) {
    return undefined;
  }
  const firstChild = firstParagraph?.children?.[0];
  if (!isHastText(firstChild)) {
    return undefined;
  }
  const match = /^\[!(CURRENT|EVIDENCE)\](?=\s|$)/.exec(firstChild.value);
  if (!match) {
    return undefined;
  }
  return {
    kind: match[1] as StatusDirectiveKind,
    strippedText: firstChild.value.slice(match[0].length),
  };
}
