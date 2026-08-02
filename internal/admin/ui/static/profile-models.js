import { modelReferences } from "./profile-readiness.js";
import { profileSectionHref } from "./routes.js";

export function validateModelMutation(draft, previousID, nextID) {
  const previous = String(previousID ?? "");
  const next = String(nextID ?? "");
  if (!previous || previous === next) {
    return { allowed: true, references: [] };
  }
  const references = modelReferences(draft, previous);
  return {
    allowed: references.length === 0,
    references,
  };
}

export function openModelReferenceDialog(root, draft, modelID, references) {
  const dialog = element("dialog", "reference-dialog");
  const panel = element("section", "stack dialog-body");
  const heading = textElement("h2", "模型仍被其他功能使用");
  heading.id = `model-reference-${draft.id}-${safeFragment(modelID)}`;
  dialog.setAttribute("aria-labelledby", heading.id);
  panel.append(
    heading,
    textElement(
      "p",
      `请先处理以下对 ${modelID} 的引用，再删除或修改模型 ID。`,
      "muted",
    ),
  );
  const list = element("ul", "reference-list");
  for (const reference of references) {
    const item = element("li");
    const link = textElement("a", reference.label);
    link.setAttribute(
      "href",
      profileSectionHref(draft.id, reference.section),
    );
    item.append(link);
    list.append(item);
  }
  const close = textElement("button", "知道了", "button");
  close.type = "button";
  const closeDialog = () => {
    dialog.close();
    dialog.remove();
  };
  close.addEventListener("click", closeDialog);
  dialog.addEventListener("cancel", (event) => {
    event.preventDefault();
    closeDialog();
  });
  panel.append(list, close);
  dialog.append(panel);
  root.append(dialog);
  dialog.showModal();
  return dialog;
}

function safeFragment(value) {
  return String(value).replace(/[^a-zA-Z0-9_-]+/g, "-") || "model";
}

function element(tag, className = "") {
  const value = document.createElement(tag);
  value.className = className;
  return value;
}

function textElement(tag, text, className = "") {
  const value = element(tag, className);
  value.textContent = text;
  return value;
}
