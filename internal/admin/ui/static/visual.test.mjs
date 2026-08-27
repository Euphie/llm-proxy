import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";

import {
  PROFILE_ICON_PALETTES,
  profileIconVisual,
  stablePaletteIndex,
} from "./visual.js";

const styles = readFileSync(new URL("./styles.css", import.meta.url), "utf8");

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

test("application flash is a compact opaque viewport toast", () => {
  const flashRule = styles.match(/\.app-flash\s*\{[^}]*\}/)?.[0] ?? "";
  assert.match(flashRule, /top:\s*50%;/);
  assert.match(flashRule, /left:\s*50%;/);
  assert.match(flashRule, /transform:\s*translate\(-50%, -50%\);/);
  assert.doesNotMatch(flashRule, /right:/);
  assert.match(flashRule, /width:\s*max-content;/);
  assert.match(flashRule, /max-width:/);
  assert.doesNotMatch(flashRule, /backdrop-filter:/);

  const successRule =
    styles.match(/\.app-flash\.success-banner\s*\{[^}]*\}/)?.[0] ?? "";
  assert.match(successRule, /background:\s*#[0-9a-f]{6};/i);
});

test("evaluation catalog progress stays compact inside the routing card", () => {
  const progressRule =
    styles.match(/\.strategy-catalog-progress\s*\{[^}]*\}/)?.[0] ?? "";
  assert.match(progressRule, /padding:/);
  assert.match(progressRule, /border-radius:/);
  assert.match(progressRule, /background:/);
  assert.doesNotMatch(progressRule, /position:\s*fixed/);

  const trackRule =
    styles.match(/\.strategy-catalog-progress-track\s*\{[^}]*\}/)?.[0] ?? "";
  assert.match(trackRule, /overflow:\s*hidden/);
  assert.match(trackRule, /border-radius:\s*999px/);

  const loadingRule =
    styles.match(/\.evaluation-catalog-update\.is-loading::before\s*\{[^}]*\}/)?.[0] ?? "";
  assert.match(loadingRule, /border-top-color:/);
  assert.match(loadingRule, /animation:\s*evaluation-catalog-spin/);
  assert.match(styles, /@keyframes\s+evaluation-catalog-spin/);
});

test("strategy generation disclosure stays readable on narrow screens", () => {
  const overviewRule =
    styles.match(/\.strategy-catalog-overview\s*\{[^}]*\}/)?.[0] ?? "";
  assert.match(overviewRule, /max-width:\s*100%/);
  assert.match(overviewRule, /overflow-wrap:\s*anywhere/);

  const disclosureRule =
    styles.match(/\.strategy-generator-advanced,\s*\n\.strategy-configuration-details\s*\{[^}]*\}/)?.[0] ?? "";
  assert.match(disclosureRule, /max-width:\s*100%/);

  const summaryRule =
    styles.match(/\.strategy-recommendation-summary\s*\{[^}]*\}/)?.[0] ?? "";
  assert.match(summaryRule, /min-width:\s*0/);

  const listRule =
    styles.match(/\.strategy-recommendation-list\s*\{[^}]*\}/)?.[0] ?? "";
  assert.match(listRule, /overflow-wrap:\s*anywhere/);

  const taskRowsRule =
    styles.match(/\.strategy-task-route-rows\s*\{[^}]*\}/)?.[0] ?? "";
  assert.match(taskRowsRule, /grid-template-columns:\s*repeat\(auto-fit/);

  const stateRule =
    styles.match(/\.strategy-state-comparison\s*\{[^}]*\}/)?.[0] ?? "";
  assert.match(stateRule, /grid-template-columns:\s*repeat\(auto-fit/);
});

test("strategy activation guidance is compact and wraps safely", () => {
  const usageRule =
    styles.match(/\.strategy-usage-panel\s*\{[^}]*\}/)?.[0] ?? "";
  assert.match(usageRule, /min-width:\s*0/);

  const codeRule =
    styles.match(/\.strategy-usage-code\s*\{[^}]*\}/)?.[0] ?? "";
  assert.match(codeRule, /width:\s*max-content/);
  assert.match(codeRule, /max-width:\s*100%/);

  const stepsRule =
    styles.match(/\.strategy-lifecycle-steps\s*\{[^}]*\}/)?.[0] ?? "";
  assert.match(stepsRule, /display:\s*grid/);
  assert.match(stepsRule, /grid-template-columns:\s*repeat\(auto-fit,/);
});

test("session routing flow uses a vertical timeline without horizontal scrolling", () => {
	const layoutRule =
		styles.match(/\.routing-flow-layout\s*\{[^}]*\}/)?.[0] ?? "";
	assert.match(layoutRule, /grid-template-columns:\s*minmax\(12rem,/);

	const turnsRule =
		styles.match(/\.routing-flow-turns\s*\{[^}]*\}/)?.[0] ?? "";
	assert.match(turnsRule, /max-height:/);
	assert.match(turnsRule, /overflow:\s*auto/);

	const focusRule =
		styles.match(/\.routing-flow-focus\s*\{[^}]*\}/)?.[0] ?? "";
	assert.match(focusRule, /grid-template-columns:\s*minmax\(18rem,/);

	const trackRule =
		styles.match(/\.routing-flow-track\s*\{[^}]*\}/)?.[0] ?? "";
	assert.match(trackRule, /display:\s*grid/);
	assert.doesNotMatch(trackRule, /overflow-x:\s*auto/);
	assert.match(trackRule, /min-width:\s*0/);

	const nodeRule =
		styles.match(/\.routing-flow-node\s*\{[^}]*\}/)?.[0] ?? "";
	assert.match(nodeRule, /width:\s*100%/);
	assert.match(nodeRule, /overflow-wrap:\s*anywhere/);

	const stepRule =
		styles.match(/\.routing-flow-step\s*\{[^}]*\}/)?.[0] ?? "";
	assert.match(stepRule, /position:\s*relative/);
	assert.match(stepRule, /padding-left:/);

	const mobile = styles.match(/@media\s*\(max-width:\s*48rem\)[\s\S]*$/)?.[0] ?? "";
	assert.match(mobile, /\.routing-flow-layout/);
	assert.match(mobile, /grid-template-columns:\s*1fr/);
	assert.match(mobile, /\.routing-flow-focus/);
});

test("routing profile actions stay inline and never cover the form", () => {
	const actionRule =
		styles.match(/\.routing-profile-actions\s*\{[^}]*\}/)?.[0] ?? "";
	assert.doesNotMatch(actionRule, /position:\s*(?:sticky|fixed)/);
	assert.doesNotMatch(actionRule, /backdrop-filter/);
	assert.doesNotMatch(actionRule, /box-shadow/);
	assert.match(actionRule, /border-top:/);
	assert.match(actionRule, /background:\s*transparent/);
});
