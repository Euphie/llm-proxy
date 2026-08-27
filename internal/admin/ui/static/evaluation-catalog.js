export function evaluationCatalogCoverage(catalog, models) {
  const results = Array.isArray(catalog?.results) ? catalog.results : [];
  const domains = [...new Set(
    results.map((result) => String(result?.domain || "")).filter(Boolean),
  )].sort();
  const byModel = new Map();
  for (const result of results) {
    const modelID = String(result?.model_id || "");
    const domain = String(result?.domain || "");
    if (!modelID || !domain) continue;
    const covered = byModel.get(modelID) || new Set();
    covered.add(domain);
    byModel.set(modelID, covered);
  }

  const summary = { total: 0, full: 0, partial: 0, missing: 0, unmapped: 0 };
  const coverageModels = (models || []).map((model) => {
    const canonicalModelID = String(model?.canonical_model_id || "");
    const covered = canonicalModelID
      ? [...(byModel.get(canonicalModelID) || [])].sort()
      : [];
    let status = "unmapped";
    if (canonicalModelID && covered.length === 0) {
      status = "missing";
    } else if (canonicalModelID && domains.length > 0 && covered.length === domains.length) {
      status = "full";
    } else if (canonicalModelID && covered.length > 0) {
      status = "partial";
    }
    summary.total++;
    summary[status]++;
    return {
      id: String(model?.id || ""),
      canonical_model_id: canonicalModelID,
      status,
      domains: covered,
    };
  });
  return { domains, summary, models: coverageModels };
}

export async function runEvaluationCatalogUpdate({
  start,
  status,
  onProgress = () => {},
  wait = (delay) => new Promise((resolve) => setTimeout(resolve, delay)),
} = {}) {
  if (typeof start !== "function" || typeof status !== "function") {
    throw new TypeError("evaluation catalog update requires start and status functions");
  }
  let job = await start();
  onProgress(job);
  while (job?.state === "running") {
    await wait(500);
    const next = await status();
    if (job?.id && next?.id && next.id !== job.id) {
      throw new Error("公开评测更新任务已被替换，请刷新页面后重试。");
    }
    job = next;
    onProgress(job);
  }
  if (job?.state === "succeeded") {
    return job;
  }
  const error = new Error(
    job?.error || (job?.state === "cancelled"
      ? "公开评测更新已取消，仍在使用之前的数据。"
      : "公开评测更新失败，仍在使用之前的数据。"),
  );
  error.job = job;
  throw error;
}
