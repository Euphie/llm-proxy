import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const siteRoot = fileURLToPath(new URL("../", import.meta.url));
const outputDirectory = path.join(siteRoot, "diagrams");

const colors = {
  primary: ["#dbeafe", "#1e40af"],
  success: ["#dcfce7", "#166534"],
  warning: ["#fef9c3", "#854d0e"],
  error: ["#fee2e2", "#991b1b"],
  external: ["#f3e8ff", "#6b21a8"],
  process: ["#e0f2fe", "#0369a1"],
  trigger: ["#fed7aa", "#c2410c"],
  neutral: ["#f1f5f9", "#475569"],
};

function diagram(name, sections) {
  return { name, sections };
}

const diagrams = [
  diagram("system-architecture", [
    ({ title, zone }) => {
      title("title", "Mesotes 智能路由 · 当前 V2 架构");
      zone("control_zone", "阶段 1 · 控制面保存后生成新修订", 60, 120, 1480, 190);
      zone("request_zone", "阶段 2 · 请求面为每个请求冻结执行快照", 60, 350, 1480, 230);
      zone("evidence_zone", "阶段 3 · 执行后写入证据，校准结果用于后续新修订", 60, 620, 1480, 190);
    },
    ({ node, connect }) => {
      node("admin", "管理后台", 120, 185, 180, 72, "primary");
      node("profiles", "Profile 与模型目录", 370, 175, 230, 92, "process");
      node("policy", "Routing Policy\nProduction / Shadow", 650, 175, 250, 92, "warning");
      node("runtime_revision", "不可变运行时修订", 1020, 185, 240, 72, "success");
      connect("admin_profiles", "admin", "profiles");
      connect("profiles_policy", "profiles", "policy");
      connect("policy_revision", "policy", "runtime_revision");
    },
    ({ node, connect }) => {
      node("client", "客户端请求", 90, 435, 190, 72, "trigger", "ellipse");
      node("profile_boundary", "URL 选定 Profile", 330, 425, 220, 92, "primary");
      node("router", "显式透传 / model=auto", 610, 425, 250, 92, "warning", "diamond");
      node("executor", "冻结 ExecutionPlan\n分析 · 视觉 · 回答", 920, 420, 260, 102, "process");
      node("upstream", "唯一 Upstream\n节点治理由网关负责", 1290, 425, 180, 92, "external");
      connect("client_profile", "client", "profile_boundary");
      connect("profile_router", "profile_boundary", "router");
      connect("router_executor", "router", "executor");
      connect("executor_upstream", "executor", "upstream");
      connect("revision_executor", "runtime_revision", "executor", {
        from: "bottom",
        to: "top",
        strokeStyle: "dotted",
        axis: "vertical",
      });
    },
    ({ node, connect }) => {
      node("traces", "路由轨迹", 105, 690, 190, 72, "success");
      node("physical_calls", "物理调用与费用", 385, 690, 220, 72, "success");
      node("local_evidence", "本地质量 / 稳定性证据", 705, 685, 250, 82, "success");
      node("reconciler", "策略校准器", 1055, 690, 190, 72, "external");
      connect("traces_calls", "traces", "physical_calls");
      connect("calls_evidence", "physical_calls", "local_evidence");
      connect("evidence_reconciler", "local_evidence", "reconciler");
    },
  ]),
  diagram("online-routing-flow", [
    ({ title, zone }) => {
      title("title", "model=auto 在线请求链路");
      zone("entry_zone", "阶段 1 · 入口与模式选择", 60, 120, 1480, 180);
      zone("decision_zone", "阶段 2 · model=auto 分析与候选", 60, 340, 1480, 240);
      zone("execution_zone", "阶段 3 · 合格候选冻结计划并执行", 60, 620, 1480, 190);
    },
    ({ node, connect, label }) => {
      node("request", "请求进入", 105, 180, 200, 70, "trigger", "ellipse");
      node("select_profile", "URL 选定 Profile", 365, 174, 220, 82, "primary");
      node("explicit_decision", "model 是 auto？", 660, 165, 220, 100, "warning", "diamond");
      node("explicit_forward", "显式模型透传", 1040, 174, 250, 82, "process");
      connect("request_profile", "request", "select_profile");
      connect("profile_explicit", "select_profile", "explicit_decision");
      connect("explicit_forward_arrow", "explicit_decision", "explicit_forward");
      label("explicit_yes", "是 → 进入阶段 2", 660, 273, 130);
      label("explicit_no", "否", 930, 184, 54);
    },
    ({ node, connect, label }) => {
      node("session_context", "读取 Session 上下文", 105, 420, 220, 72, "primary");
      node("classification", "任务分析\n类型 · 难度 · 风险 · 置信度", 375, 405, 250, 100, "external");
      node("route_mapping", "任务映射到 Route", 680, 420, 230, 72, "process");
      node("hard_gates", "硬门槛筛选\n能力 · Production · 质量 · 稳定性", 965, 400, 280, 110, "warning", "diamond");
      node("safe_fallback", "无合格候选：安全基线", 1290, 420, 220, 72, "error");
      connect("session_classification", "session_context", "classification");
      connect("classification_route", "classification", "route_mapping");
      connect("route_gates", "route_mapping", "hard_gates");
      connect("gates_fallback", "hard_gates", "safe_fallback");
      label("gates_none", "无合格候选", 1190, 425, 100);
    },
    ({ node, connect, label }) => {
      node("score", "合格候选评分", 95, 685, 200, 72, "success");
      node("freeze_plan", "冻结 ExecutionPlan\n候选 · 预算 · deadline · 费用", 350, 670, 280, 102, "primary");
      node("auxiliary", "辅助调用\n分析 / 视觉", 690, 680, 230, 82, "external");
      node("answer", "回答尝试", 980, 685, 220, 72, "process");
      node("commit", "ClientCommit / 结束", 1260, 695, 230, 52, "success", "ellipse");
      connect("score_plan", "score", "freeze_plan");
      connect("plan_auxiliary", "freeze_plan", "auxiliary");
      connect("auxiliary_answer", "auxiliary", "answer");
      connect("answer_commit", "answer", "commit");
      label("gates_pass", "有合格候选 → 进入阶段 3", 1025, 555, 200);
    },
  ]),
  diagram("vision-processing-flow", [
    ({ title, zone }) => {
      title("title", "视觉预处理决策与调用链路");
      zone("decision_zone", "阶段 1 · 选择原生视觉或复合视觉", 60, 120, 1480, 230);
      zone("composite_zone", "阶段 2 · 复合视觉缓存命中与识图调用在本阶段汇合", 60, 390, 1480, 240);
      zone("answer_zone", "阶段 3 · 原生图片或识图文本进入原回答模型", 60, 670, 1480, 170);
    },
    ({ node, connect, label }) => {
      node("image_request", "请求包含图片", 100, 210, 200, 72, "trigger", "ellipse");
      node("native_capability", "回答模型原生支持视觉？", 360, 195, 260, 102, "warning", "diamond");
      node("native_path", "原生图片直达回答模型", 700, 155, 260, 72, "primary");
      node("vision_enabled", "已开启视觉预处理？", 700, 260, 260, 72, "warning", "diamond");
      node("not_executable", "未开启增强：候选不可执行", 1050, 260, 300, 72, "error");
      connect("image_native", "image_request", "native_capability");
      connect("native_direct", "native_capability", "native_path", {
        from: "right",
        to: "left",
        toOffset: 0.5,
        via: [{ x: 655, y: 246 }, { x: 655, y: 191 }],
      });
      connect("native_enhancement", "native_capability", "vision_enabled", {
        from: "right",
        to: "left",
        via: [{ x: 655, y: 246 }, { x: 655, y: 296 }],
      });
      connect("enhancement_unavailable", "vision_enabled", "not_executable");
      label("native_yes", "支持", 640, 170, 60);
      label("native_no", "不支持", 630, 300, 70);
      label("enhancement_yes", "已开启 → 进入阶段 2", 720, 345, 160);
      label("enhancement_no", "未开启", 980, 270, 70);
    },
    ({ node, connect, label }) => {
      node("cache_lookup", "识图缓存查找", 100, 475, 230, 72, "success");
      node("cache_hit", "命中缓存？", 390, 460, 240, 102, "warning", "diamond");
      node("cached_description", "复用缓存描述", 710, 415, 240, 72, "success");
      node("vision_call", "调用识图模型\n只生成客观图片描述", 690, 525, 280, 82, "external");
      node("replace_image", "用识图文本替换图片", 1070, 475, 260, 72, "process");
      connect("cache_decision", "cache_lookup", "cache_hit");
      connect("cache_hit_description", "cache_hit", "cached_description", {
        from: "right",
        to: "left",
        via: [{ x: 665, y: 511 }, { x: 665, y: 451 }],
      });
      connect("cache_miss_call", "cache_hit", "vision_call", {
        from: "right",
        to: "left",
        via: [{ x: 665, y: 511 }, { x: 665, y: 566 }],
      });
      connect("cached_replace", "cached_description", "replace_image", {
        from: "right",
        to: "left",
        via: [{ x: 1010, y: 451 }, { x: 1010, y: 511 }],
      });
      connect("call_replace", "vision_call", "replace_image", {
        from: "right",
        to: "left",
        via: [{ x: 1010, y: 566 }, { x: 1010, y: 511 }],
      });
      label("cache_yes", "命中", 650, 430, 60);
      label("cache_miss", "未命中", 640, 575, 70);
    },
    ({ node, connect, label }) => {
      node("answer_input", "原生图片 / 识图文本", 110, 725, 250, 72, "primary");
      node("answer_model", "原回答模型作答", 450, 725, 240, 72, "process");
      node("success", "返回客户端", 780, 735, 230, 52, "success", "ellipse");
      node("vision_failure", "识图失败：请求失败", 1130, 690, 260, 60, "error");
      node("answer_failure", "回答失败：独立失败", 1130, 770, 260, 52, "error");
      connect("input_answer", "answer_input", "answer_model");
      connect("answer_success", "answer_model", "success");
      label("failure_boundary", "失败边界（不属于成功主链）", 1120, 655, 280);
    },
  ]),
  diagram("retry-timeout-state-machine", [
    ({ title, zone }) => {
      title("title", "重试、超时与提交边界状态机");
      zone("attempt_zone", "阶段 1 · 回答调用产生成功或失败", 60, 120, 1480, 220);
      zone("gate_zone", "阶段 2 · 失败后从左到右检查，全部为“是”才允许重试", 60, 380, 1480, 220);
      zone("terminal_zone", "阶段 3 · 按失败原因结束，或生成下一次尝试后回到阶段 1", 60, 640, 1480, 190);
    },
    ({ node, connect }) => {
      node("answer_attempt", "发起回答调用", 110, 200, 260, 72, "trigger", "ellipse");
      node("response_result", "调用结果", 450, 185, 240, 102, "warning", "diamond");
      node("commit_success", "成功并形成 ClientCommit", 800, 155, 280, 72, "success");
      node("failure_event", "失败：0 network / 5xx", 800, 255, 280, 52, "error");
      connect("attempt_result", "answer_attempt", "response_result");
      connect("result_success", "response_result", "commit_success", {
        from: "right",
        to: "left",
        via: [{ x: 740, y: 236 }, { x: 740, y: 191 }],
      });
      connect("result_failure", "response_result", "failure_event", {
        from: "right",
        to: "left",
        via: [{ x: 740, y: 236 }, { x: 740, y: 281 }],
      });
    },
    ({ node, connect, label }) => {
      node("recoverable", "错误可恢复？", 90, 455, 220, 82, "warning", "diamond");
      node("rule_match", "overload_rules\n命中？", 355, 455, 220, 82, "warning", "diamond");
      node("budget_left", "重试票据与\n调用预算可用？", 620, 455, 240, 82, "warning", "diamond");
      node("deadline_left", "共享 deadline\n未到？", 905, 455, 220, 82, "warning", "diamond");
      node("pre_commit", "尚未\nClientCommit？", 1170, 455, 220, 82, "warning", "diamond");
      connect("recoverable_rule", "recoverable", "rule_match");
      connect("rule_budget", "rule_match", "budget_left");
      connect("budget_deadline", "budget_left", "deadline_left");
      connect("deadline_commit", "deadline_left", "pre_commit");
      label("retry_yes_1", "是", 315, 475, 40);
      label("retry_yes_2", "是", 580, 475, 40);
      label("retry_yes_3", "是", 860, 475, 40);
      label("retry_yes_4", "是", 1125, 475, 40);
      label("retry_all_yes", "全部满足 → 进入阶段 3 的“下一次尝试”", 1140, 555, 340);
    },
    ({ node }) => {
      node("return_failure", "前置条件为否\n返回原始失败", 100, 700, 280, 82, "error");
      node("gateway_timeout", "共享时限耗尽\n返回 504", 460, 700, 260, 82, "error");
      node("committed_end", "已经 ClientCommit\n不能重写响应", 800, 700, 270, 82, "neutral");
      node("next_attempt", "生成下一次尝试\n同模型重试 / 计划允许时切换", 1150, 700, 330, 82, "primary");
    },
  ]),
  diagram("session-lock-lifecycle", [
    ({ title, zone }) => {
      title("title", "Session 锁定生命周期");
      zone("observe_zone", "阶段 1 · 建立稳定性", 60, 120, 1480, 190);
      zone("lock_zone", "阶段 2 · 满足任一锁定路径后复用模型", 60, 350, 1480, 220);
      zone("recheck_zone", "阶段 3 · 每个新用户任务仍需复检风险和硬能力", 60, 610, 1480, 210);
    },
    ({ node, connect, label }) => {
      node("new_session", "新 Session", 90, 190, 200, 72, "trigger", "ellipse");
      node("classify_task", "分析本轮任务", 345, 190, 220, 72, "process");
      node("highest_model", "最高能力模型？", 625, 180, 220, 92, "warning", "diamond");
      node("stable_count", "连续 3 个\n稳定任务？", 905, 180, 220, 92, "warning", "diamond");
      node("confidence", "置信度 ≥ 85%？", 1185, 180, 220, 92, "warning", "diamond");
      connect("session_classify", "new_session", "classify_task");
      connect("classify_highest", "classify_task", "highest_model");
      connect("highest_stable", "highest_model", "stable_count");
      connect("stable_confidence", "stable_count", "confidence");
      label("highest_no", "普通模型继续检查", 840, 205, 130);
    },
    ({ node, connect }) => {
      node("highest_lock_path", "最高能力模型：立即满足", 120, 390, 300, 72, "warning");
      node("normal_lock_path", "普通模型：3 次稳定 + 85% + 默认 100K", 120, 480, 360, 72, "warning");
      node("lock_model", "锁定当前模型与目录修订", 650, 430, 300, 82, "success");
      node("reuse_model", "普通任务优先复用", 1110, 435, 260, 72, "primary");
      connect("highest_lock", "highest_lock_path", "lock_model", {
        from: "right",
        to: "left",
        toOffset: 0.35,
      });
      connect("threshold_lock", "normal_lock_path", "lock_model", {
        from: "right",
        to: "left",
        toOffset: 0.65,
      });
      connect("lock_reuse", "lock_model", "reuse_model");
    },
    ({ node, connect }) => {
      node("new_user_task", "新用户任务", 100, 690, 220, 72, "trigger", "ellipse");
      node("risk_recheck", "重新分析风险与硬能力", 390, 685, 270, 82, "process");
      node("break_lock", "高风险 / 能力不符 / 主动升级？", 735, 675, 300, 102, "warning", "diamond");
      node("keep_lock", "否：保持锁定模型", 1110, 650, 260, 72, "success");
      node("unlock", "是：解锁；下轮重新规划", 1110, 745, 280, 52, "error");
      connect("task_risk", "new_user_task", "risk_recheck");
      connect("risk_break", "risk_recheck", "break_lock");
      connect("break_keep", "break_lock", "keep_lock", {
        from: "right",
        to: "left",
        via: [{ x: 1070, y: 726 }, { x: 1070, y: 686 }],
      });
      connect("break_unlock", "break_lock", "unlock", {
        from: "right",
        to: "left",
        via: [{ x: 1070, y: 726 }, { x: 1070, y: 771 }],
      });
    },
  ]),
  diagram("policy-calibration-loop", [
    ({ title, zone }) => {
      title("title", "Routing Policy 自动校准闭环");
      zone("control_zone", "阶段 1 · 管理员保存新修订并保留历史", 60, 120, 1480, 180);
      zone("runtime_zone", "阶段 2 · 新请求读取活动修订并积累 Profile 内证据", 60, 340, 1480, 210);
      zone("reconcile_zone", "阶段 3 · 使用证据校准，新修订由后续请求读取", 60, 590, 1480, 250);
    },
    ({ node, connect }) => {
      node("edit_policy", "编辑 / 生成 Policy 预览", 100, 175, 250, 82, "primary");
      node("validate_policy", "校验模型、Route 与预算", 410, 175, 270, 82, "warning");
      node("save_revision", "保存新生效修订", 750, 180, 250, 72, "success");
      node("history_copy", "不可变历史与回滚副本", 1110, 180, 280, 72, "external");
      connect("edit_validate", "edit_policy", "validate_policy");
      connect("validate_save", "validate_policy", "save_revision");
      connect("save_history", "save_revision", "history_copy", { strokeStyle: "dashed" });
    },
    ({ node, connect }) => {
      node("active_policy", "新请求读取活动修订", 105, 410, 260, 72, "process");
      node("production_shadow", "Production 在线\nShadow 只积累证据", 450, 400, 280, 92, "warning");
      node("local_metrics", "本地质量、稳定性、严重错误", 815, 405, 300, 82, "success");
      node("evidence_snapshot", "形成 Profile 内证据快照", 1200, 410, 260, 72, "success");
      connect("active_eligibility", "active_policy", "production_shadow");
      connect("eligibility_metrics", "production_shadow", "local_metrics");
      connect("metrics_snapshot", "local_metrics", "evidence_snapshot");
    },
    ({ node, connect, label }) => {
      node("reliable_evidence", "证据可靠且样本充分？", 105, 680, 260, 92, "warning", "diamond");
      node("debounce", "10 分钟防抖\n两次一致确认", 430, 650, 220, 82, "process");
      node("critical_demotion", "严重恶化\n立即撤销资格", 430, 755, 220, 72, "error");
      node("cooldown", "普通变更\n6 小时冷却", 715, 650, 220, 82, "neutral");
      node("new_revision", "写入新的 Policy 修订", 1040, 690, 280, 72, "success");
      node("auto_switch", "auto_update_policy\n由管理员控制", 1340, 685, 170, 82, "neutral");
      connect("evidence_debounce", "reliable_evidence", "debounce", {
        from: "right",
        to: "left",
        toOffset: 0.35,
      });
      connect("debounce_cooldown", "debounce", "cooldown");
      connect("evidence_critical", "reliable_evidence", "critical_demotion", {
        from: "right",
        to: "left",
        toOffset: 0.65,
        strokeStyle: "dashed",
      });
      connect("cooldown_revision", "cooldown", "new_revision");
      connect("critical_revision", "critical_demotion", "new_revision");
      label("normal_change", "普通变化", 370, 655, 80);
      label("critical_change", "严重恶化", 365, 785, 80);
      label("next_request_note", "新修订由后续请求读取", 1050, 785, 260);
    },
  ]),
  diagram("observability-troubleshooting", [
    ({ title, zone }) => {
      title("title", "一次请求的统计与排障链路");
      zone("trace_zone", "阶段 1 · 按 request_trace_id / 会话 ID 串联请求阶段", 60, 130, 1480, 180);
      zone("calls_zone", "阶段 2 · 汇总物理调用，右侧列出常见失败状态", 60, 350, 1480, 220);
      zone("diagnosis_zone", "阶段 3 · 使用会话、轨迹、调用账本和安全日志解释结果", 60, 610, 1480, 200);
    },
    ({ node, connect }) => {
      node("client", "客户端", 110, 185, 180, 72, "trigger");
      node("proxy", "代理请求入口", 360, 185, 200, 72, "primary");
      node("analyzer", "任务分析", 650, 185, 190, 72, "external");
      node("vision", "视觉预处理", 930, 185, 200, 72, "external");
      node("answer", "回答执行", 1220, 185, 190, 72, "process");
      connect("client_proxy", "client", "proxy");
      connect("proxy_analyzer", "proxy", "analyzer");
      connect("analyzer_vision", "analyzer", "vision");
      connect("vision_answer", "vision", "answer");
    },
    ({ node, connect }) => {
      node("aux_calls", "物理调用\n分析 + 识图 + 回答 + 重试 / 切换", 220, 415, 360, 92, "process");
      node("upstream", "唯一 Upstream", 680, 425, 220, 72, "external");
      node("answer_calls", "调用结果\n状态 · 耗时 · 费用", 1000, 415, 260, 92, "success");
      node("network_zero", "0 / network\n无有效 HTTP 状态", 1330, 385, 180, 72, "error");
      node("timeout_504", "504\n共享 deadline 耗尽", 1330, 485, 180, 62, "error");
      connect("aux_upstream", "aux_calls", "upstream");
      connect("upstream_result", "upstream", "answer_calls");
    },
    ({ node, connect }) => {
      node("session_card", "会话卡片\n每次请求独立编号", 145, 680, 250, 82, "primary");
      node("route_trace", "路由轨迹\n阶段、候选排除与最终状态", 480, 665, 290, 102, "success");
      node("call_ledger", "物理调用账本\n状态、耗时、费用", 865, 665, 260, 102, "success");
      node("debug_logs", "安全 Debug 日志\n不含正文、图片和鉴权", 1200, 665, 260, 102, "neutral");
      connect("session_trace", "session_card", "route_trace");
      connect("trace_ledger", "route_trace", "call_ledger");
      connect("ledger_logs", "call_ledger", "debug_logs");
    },
  ]),
];

class DiagramBuilder {
  constructor(diagramIndex) {
    this.diagramIndex = diagramIndex;
    this.sectionIndex = 0;
    this.elementIndex = 0;
    this.elements = [];
  }

  startSection(sectionIndex) {
    this.sectionIndex = sectionIndex;
    this.elementIndex = 0;
  }

  seed() {
    this.elementIndex += 1;
    return this.diagramIndex * 1_000_000 + this.sectionIndex * 100_000 + this.elementIndex;
  }

  base(id, type, x, y, width, height, palette, options = {}) {
    const [backgroundColor, strokeColor] = colors[palette];
    return {
      id,
      type,
      x,
      y,
      width,
      height,
      angle: 0,
      strokeColor,
      backgroundColor,
      fillStyle: "solid",
      strokeWidth: 2,
      strokeStyle: options.strokeStyle ?? null,
      roughness: 0,
      opacity: options.opacity ?? 100,
      groupIds: options.groupIds ?? [],
      roundness: type === "rectangle" ? { type: 3 } : type === "arrow" ? { type: 2 } : null,
      seed: this.seed(),
      version: 1,
      isDeleted: false,
      boundElements: null,
      updated: 1,
      link: null,
      locked: false,
    };
  }

  title(id, text) {
    this.elements.push(this.text(id, text, 70, 42, 1460, 40, 28, "#1e293b", null));
  }

  label(id, value, x, y, width = 100) {
    this.elements.push(this.text(id, value, x, y, width, 22, 14, "#64748b", null));
  }

  zone(id, label, x, y, width, height) {
    const shape = this.base(id, "rectangle", x, y, width, height, "neutral", {
      opacity: 30,
      strokeStyle: "dashed",
    });
    this.elements.push(shape);
    this.elements.push(this.text(`${id}_label`, label, x + 24, y + 18, width - 48, 30, 24, "#475569", null, "left"));
  }

  node(id, label, x, y, width, height, palette = "process", type = "rectangle") {
    const shape = this.base(id, type, x, y, width, height, palette);
    const lines = label.split("\n").length;
    const fontSize = lines > 1 ? 16 : 20;
    const textHeight = lines * Math.round(fontSize * 1.25);
    const labelElement = this.text(
      `${id}_label`,
      label,
      x + 12,
      y + (height - textHeight) / 2,
      width - 24,
      textHeight,
      fontSize,
      "#334155",
      id,
    );
    shape.boundElements = [{ id: labelElement.id, type: "text" }];
    this.elements.push(shape, labelElement);
  }

  text(id, value, x, y, width, height, fontSize, strokeColor, containerId, textAlign = "center") {
    return {
      ...this.base(id, "text", x, y, width, height, "neutral"),
      strokeColor,
      backgroundColor: "#f1f5f9",
      text: value,
      fontSize,
      fontFamily: 2,
      textAlign,
      verticalAlign: "middle",
      containerId,
      originalText: value,
      autoResize: false,
      lineHeight: 1.25,
      baseline: Math.round(fontSize * 0.9),
    };
  }

  connect(id, startId, endId, options = {}) {
    const start = this.elements.find((element) => element.id === startId);
    const end = this.elements.find((element) => element.id === endId);
    if (!start || !end) throw new Error(`${id} has an unknown endpoint.`);
    const startPoint = options.from
      ? anchorPoint(start, options.from, options.fromOffset)
      : boundaryPoint(start, end);
    const endPoint = options.to
      ? anchorPoint(end, options.to, options.toOffset)
      : boundaryPoint(end, start);
    const deltaX = endPoint.x - startPoint.x;
    const deltaY = endPoint.y - startPoint.y;
    const points = routePoints(startPoint, endPoint, options);
    const arrow = {
      ...this.base(id, "arrow", startPoint.x, startPoint.y, Math.abs(deltaX), Math.abs(deltaY), "neutral", options),
      points,
      lastCommittedPoint: null,
      startBinding: { elementId: startId, gap: 8, focus: 0 },
      endBinding: { elementId: endId, gap: 8, focus: 0 },
      startArrowhead: null,
      endArrowhead: "arrow",
      elbowed: points.length > 2,
    };
    start.boundElements = [...(start.boundElements ?? []), { id, type: "arrow" }];
    end.boundElements = [...(end.boundElements ?? []), { id, type: "arrow" }];
    this.elements.push(arrow);
  }
}

function anchorPoint(element, side, offset = 0.5) {
  const normalizedOffset = Math.max(0.1, Math.min(0.9, offset ?? 0.5));
  switch (side) {
    case "top":
      return { x: element.x + element.width * normalizedOffset, y: element.y };
    case "bottom":
      return { x: element.x + element.width * normalizedOffset, y: element.y + element.height };
    case "left":
      return { x: element.x, y: element.y + element.height * normalizedOffset };
    case "right":
      return { x: element.x + element.width, y: element.y + element.height * normalizedOffset };
    default:
      throw new Error(`Unknown anchor side: ${side}`);
  }
}

function routePoints(startPoint, endPoint, options) {
  const deltaX = endPoint.x - startPoint.x;
  const deltaY = endPoint.y - startPoint.y;
  if (options.via?.length) {
    return [
      [0, 0],
      ...options.via.map(({ x, y }) => [x - startPoint.x, y - startPoint.y]),
      [deltaX, deltaY],
    ];
  }
  if (deltaX === 0 || deltaY === 0) return [[0, 0], [deltaX, deltaY]];
  if (options.axis === "vertical" || (options.axis !== "horizontal" && Math.abs(deltaY) > Math.abs(deltaX))) {
    return [[0, 0], [0, deltaY / 2], [deltaX, deltaY / 2], [deltaX, deltaY]];
  }
  return [[0, 0], [deltaX / 2, 0], [deltaX / 2, deltaY], [deltaX, deltaY]];
}

function boundaryPoint(source, target) {
  const sourceCenter = { x: source.x + source.width / 2, y: source.y + source.height / 2 };
  const targetCenter = { x: target.x + target.width / 2, y: target.y + target.height / 2 };
  const deltaX = targetCenter.x - sourceCenter.x;
  const deltaY = targetCenter.y - sourceCenter.y;
  const horizontalScale = Math.abs(deltaX) / Math.max(source.width / 2, 1);
  const verticalScale = Math.abs(deltaY) / Math.max(source.height / 2, 1);
  const scale = 1 / Math.max(horizontalScale, verticalScale, 1);
  return {
    x: sourceCenter.x + deltaX * scale,
    y: sourceCenter.y + deltaY * scale,
  };
}

function document(elements) {
  return {
    type: "excalidraw",
    version: 2,
    source: "claude-code",
    elements,
    appState: { viewBackgroundColor: "#ffffff" },
    files: {},
  };
}

await mkdir(outputDirectory, { recursive: true });
for (const [diagramIndex, definition] of diagrams.entries()) {
  const output = path.join(outputDirectory, `${definition.name}.excalidraw`);
  const builder = new DiagramBuilder(diagramIndex + 1);
  await writeFile(output, `${JSON.stringify(document([]), null, 2)}\n`);
  for (const [sectionIndex, buildSection] of definition.sections.entries()) {
    builder.startSection(sectionIndex + 1);
    buildSection({
      title: builder.title.bind(builder),
      label: builder.label.bind(builder),
      zone: builder.zone.bind(builder),
      node: builder.node.bind(builder),
      connect: builder.connect.bind(builder),
    });
    await writeFile(output, `${JSON.stringify(document(builder.elements), null, 2)}\n`);
  }
}
