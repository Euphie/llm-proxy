export function createWindowChrome(titleText = "llm-proxy") {
  const titlebar = document.createElement("div");
  titlebar.className = "window-titlebar";

  const controls = document.createElement("div");
  controls.className = "window-controls";
  controls.setAttribute("aria-hidden", "true");
  for (const color of ["close", "minimize", "zoom"]) {
    const light = document.createElement("span");
    light.className = `window-control window-control-${color}`;
    controls.append(light);
  }

  const title = document.createElement("span");
  title.className = "window-title";
  title.textContent = titleText;
  titlebar.append(controls, title);
  return titlebar;
}

export function createWindowFrame({
  titleText = "llm-proxy",
  className = "",
  bodyClassName = "",
  children = [],
} = {}) {
  const frame = document.createElement("section");
  frame.className = ["mac-window", className].filter(Boolean).join(" ");

  const body = document.createElement("div");
  body.className = ["mac-window-body", bodyClassName]
    .filter(Boolean)
    .join(" ");
  body.append(...children);

  frame.append(createWindowChrome(titleText), body);
  return frame;
}
