import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

import {
  PROFILE_ICON_PALETTES,
  profileIconVisual,
  stablePaletteIndex,
} from "./visual.js";

const staticDir = dirname(fileURLToPath(import.meta.url));

test("stablePaletteIndex normalizes a slug and stays inside the palette", () => {
  const index = stablePaletteIndex("coding");

  assert.equal(index, stablePaletteIndex("coding"));
  assert.equal(index, stablePaletteIndex(" CODING "));
  assert.ok(index >= 0);
  assert.ok(index < PROFILE_ICON_PALETTES.length);
});

test("profileIconVisual keeps a Unicode label and palette stable", () => {
  const profile = {
    slug: "kimi",
    display_name: "月之暗面",
  };

  const first = profileIconVisual(profile);
  const second = profileIconVisual(profile);

  assert.equal(first.label, "月");
  assert.deepEqual(first, second);
  assert.match(first.className, /^profile-icon-/);
});

test("profileIconVisual falls back safely for an empty Profile", () => {
  assert.deepEqual(profileIconVisual({}), {
    label: "?",
    className: "profile-icon-blue",
  });
  assert.equal(stablePaletteIndex("", 0), 0);
});

test("hidden elements stay hidden when components define display styles", () => {
  const css = readFileSync(join(staticDir, "styles.css"), "utf8");

  assert.match(css, /\[hidden\]\s*{[^}]*display:\s*none\s*!important;/s);
});

test("two-column form fields stay top-aligned when help text heights differ", () => {
  const css = readFileSync(join(staticDir, "styles.css"), "utf8");

  assert.match(css, /\.form-grid\s*{[^}]*align-items:\s*start;/s);
  assert.match(css, /\.form-field label,\s*legend\s*{[^}]*line-height:\s*1\.4;/s);
});

test("aggregate errors open in the center of the viewport", () => {
  const css = readFileSync(join(staticDir, "styles.css"), "utf8");

  assert.match(css, /\.error-dialog\s*{[^}]*top:\s*50%;/s);
  assert.match(css, /\.error-dialog\s*{[^}]*left:\s*50%;/s);
  assert.match(css, /\.error-dialog\s*{[^}]*transform:\s*translate\(-50%,\s*-50%\);/s);
});
