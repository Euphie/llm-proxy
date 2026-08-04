const microUSDPerUSD = 1000000n;
const maxSafeInteger = BigInt(Number.MAX_SAFE_INTEGER);

export function microUSDToUSDInput(value) {
  if (value === null || value === undefined || value === "") {
    return "";
  }
  const parsed = Number(value);
  if (!Number.isSafeInteger(parsed) || parsed < 0) {
    throw new Error("微美元金额必须是安全的非负整数。");
  }
  const microUSD = BigInt(parsed);
  const whole = microUSD / microUSDPerUSD;
  const fraction = String(microUSD % microUSDPerUSD)
    .padStart(6, "0")
    .replace(/0+$/, "");
  return fraction ? `${whole}.${fraction}` : String(whole);
}

export function parseUSDToMicroUSD(value, label, { optional = false } = {}) {
  const raw = String(value ?? "");
  if (raw === "" && optional) {
    return null;
  }
  if (raw === "" || raw.trim() !== raw || !/^\d+(?:\.\d{1,6})?$/.test(raw)) {
    throw new Error(`${label}必须是非负美元金额，最多保留 6 位小数。`);
  }
  const [whole, fraction = ""] = raw.split(".");
  const microUSD = BigInt(whole) * microUSDPerUSD +
    BigInt(fraction.padEnd(6, "0"));
  if (microUSD > maxSafeInteger) {
    throw new Error(`${label}超过安全范围。`);
  }
  return Number(microUSD);
}

export function formatMicroUSD(value) {
  return `$${microUSDToUSDInput(value ?? 0)}`;
}
