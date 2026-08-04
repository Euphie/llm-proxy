import assert from "node:assert/strict";
import test from "node:test";

import {
	dimensionLabel,
	normalizeModelPerformanceFilters,
} from "./stats-models.js";

test("model performance filters omit blanks and normalize dates", () => {
	assert.deepEqual(normalizeModelPerformanceFilters({
		profile_id: " 7 ", model: " fast ", task_type: " ", difficulty: "hard",
		from: "2026-08-01", to: "", page: 2, page_size: 25,
	}), {
		profile_id: "7", model: "fast", difficulty: "hard",
		page: "2", page_size: "25", from: "2026-08-01T00:00:00.000Z",
	});
});

test("five model quality dimensions have concise Chinese labels", () => {
	assert.deepEqual([
		"correctness", "completeness", "instruction_following",
		"format_tool_safety", "task_completion",
	].map(dimensionLabel), [
		"正确性", "完整性", "指令遵循", "格式与工具安全", "任务完成度",
	]);
});
