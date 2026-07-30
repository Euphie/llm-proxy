import assert from "node:assert/strict";
import test from "node:test";

import {
  PROFILE_ICON_PALETTES,
  profileIconVisual,
  stablePaletteIndex,
} from "./visual.js";

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
