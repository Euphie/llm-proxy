import assert from "node:assert/strict";
import test from "node:test";

import {
  formatMicroUSD,
  microUSDToUSDInput,
  parseUSDToMicroUSD,
} from "./money.js";

test("USD inputs round-trip exactly through integer micro-USD", () => {
  const cases = [
    [0, "0"],
    [1, "0.000001"],
    [100000, "0.1"],
    [4250000, "4.25"],
    [9007199254740991, "9007199254.740991"],
  ];
  for (const [microUSD, usd] of cases) {
    assert.equal(microUSDToUSDInput(microUSD), usd);
    assert.equal(parseUSDToMicroUSD(usd, "金额"), microUSD);
  }
  assert.equal(microUSDToUSDInput(""), "");
  assert.equal(
    parseUSDToMicroUSD("", "选填金额", { optional: true }),
    null,
  );
});

test("USD inputs reject ambiguous or inexact values", () => {
  for (const value of ["", "-1", "1.0000001", "1e-3", "NaN", " 1 "]) {
    assert.throws(() => parseUSDToMicroUSD(value, "预算"), /预算/);
  }
  assert.throws(
    () => parseUSDToMicroUSD("9007199254.740992", "预算"),
    /安全范围/,
  );
});

test("micro-USD display uses readable exact dollar values", () => {
  assert.equal(formatMicroUSD(30126), "$0.030126");
  assert.equal(formatMicroUSD(250000), "$0.25");
  assert.equal(formatMicroUSD(undefined), "$0");
});
