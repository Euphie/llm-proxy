import { helpHref } from "./routes.js";

const helpTopics = [
  ["overview", "快速开始"],
  ["profiles-models", "Profile 与模型目录"],
  ["intelligent-routing", "Routing Policy"],
  ["vision", "视觉预处理"],
  ["reliability", "重试与超时"],
  ["agents", "Agent 配置"],
  ["statistics", "统计与排障"],
  ["glossary", "术语表"],
];

export function renderHelpPage(root, topic = "overview") {
  const shell = element("div", "help-detail-layout");
  const navigation = element("nav", "help-section-nav");
  navigation.setAttribute("aria-label", "帮助主题");
  for (const [id, label] of helpTopics) {
    const link = linkElement(label, helpHref(id));
    if (id === topic) link.setAttribute("aria-current", "page");
    navigation.append(link);
  }

  const content = element("section", "help-topic-content");
  content.append(helpContent(topic));
  shell.append(navigation, content);
  root.replaceChildren(shell);
}

function helpContent(topic) {
  switch (topic) {
  case "profiles-models":
    return profilesModelsHelp();
  case "intelligent-routing":
    return routingPolicyHelp();
  case "vision":
    return visionHelp();
  case "reliability":
    return reliabilityHelp();
  case "agents":
    return agentsHelp();
  case "statistics":
    return statisticsHelp();
  case "glossary":
    return glossaryHelp();
  default:
    return overviewHelp();
  }
}

function overviewHelp() {
  return helpPage(
    "快速开始",
    "后台围绕 Profile 工作。先完成可用连接，再按需开启 model=auto、视觉预处理和容错。",
    [
      listSection("推荐操作顺序", [
        "创建 Profile，选择协议并填写唯一 Upstream。",
        "验证显式模型请求；这类请求不会进入智能路由。",
        "录入模型目录，补齐能力、上下文、价格和状态。",
        "需要 model=auto 时配置并保存线上 Routing Policy。",
        "按需开启视觉预处理、overload_rules 和 Agent 配置。",
        "最后从统计页核对路由轨迹、物理调用、费用和失败阶段。",
      ], true),
      diagramSection(
        "system-architecture.svg",
        "当前 V2 系统架构",
        "控制面发布新修订，请求面冻结执行快照，证据面记录轨迹和物理调用。",
      ),
      noteSection("显式模型与 model=auto", [
        "调用方指定具体 model 时，代理按当前 Profile 转发，不读取 Routing Policy。",
        "只有 model=auto 才执行任务分析、候选筛选、Session 复用和执行计划。",
        "Profile 是永久隔离边界：连接、模型目录、策略、视觉和容错配置都不会跨 Profile 共享。",
      ]),
    ],
    ["打开 Profiles", "/_admin/profiles"],
  );
}

function profilesModelsHelp() {
  return helpPage(
    "Profile 与模型目录",
    "Profile 固定协议和上游边界；模型目录提供当前运行时可验证的模型事实。",
    [
      listSection("主要操作", [
        "先保存连接配置，再用测试请求验证上游、协议和密钥传递方式。",
        "录入精确且区分大小写的模型 ID，并补齐能力、上下文、最大输出和价格。",
        "将模型标记为 available、offline 或 retired；被引用的模型应先解除 Policy 或视觉配置引用。",
        "只做显式模型透传时可以不建立完整目录；model=auto 必须能解析所需模型事实。",
      ], true),
      tableSection("模型目录修订", ["变化", "运行时结果"], [
        ["保存模型事实或状态", "生成新的模型目录修订；后续请求立即读取新修订。"],
        ["修改线上 Policy", "生成新的运行时修订，并校验引用的模型目录修订。"],
        ["更新正在使用的模型", "先处理 Policy、视觉模型和角色引用，避免保存校验失败。"],
      ]),
      noteSection("关键边界", [
        "模型目录是能力和价格事实，不保存模型本身或上游密钥。",
        "状态为 offline 或 retired 的模型不能继续作为新的在线候选。",
      ]),
    ],
    ["打开 Profiles", "/_admin/profiles"],
  );
}

function routingPolicyHelp() {
  return helpPage(
    "线上 Routing Policy",
    "V2 只有一份当前生效的 Policy。智能生成只产生预览；载入、检查并保存后才立即生效。",
    [
      noteSection("保存并立即生效", [
        "保存会校验角色、任务映射、Route、候选、预算和模型目录引用。",
        "成功后新请求立即使用新运行时修订；在途请求继续使用进入时冻结的快照。",
        "每次变更会写入不可变历史；回滚会复制旧内容并创建一个新的生效版本。",
      ]),
      diagramSection(
        "online-routing-flow.svg",
        "model=auto 在线请求链路",
        "只有 model=auto 进入任务分析、候选筛选和冻结执行计划；显式模型直接透传。",
      ),
      tableSection("模型角色", ["角色", "用途"], [
        ["参与模型", "允许被 Route 引用的模型集合。"],
        ["任务分析模型", "为 model=auto 判断任务类型、难度、风险和置信度。"],
        ["强模型基线", "分析回退、高风险或普通候选全部不合格时的安全下限。"],
      ]),
      tableSection("任务映射与 Route", ["配置", "作用"], [
        ["任务映射", "把任务类型与难度映射到 Route；未命中时使用默认 Route。"],
        ["硬门槛", "先检查能力、生产资格、质量、稳定性和严重错误率。"],
        ["权重", "只有通过全部硬门槛的候选才比较质量、稳定性、成本效率和性能。"],
      ]),
      noteSection("Production 与 Shadow", [
        "Production 候选有生产资格，通过当前请求硬门槛后可以接收在线流量。",
        "Shadow 候选只能积累评测或运行证据，不会因价格低而直接接收在线流量。",
        "启用自动更新后，可靠本地证据可按冷却和确认规则调整生产资格；证据恶化时可以立即撤销。",
      ]),
      tableSection("统一尝试预算", ["字段", "限制"], [
        ["主回答尝试", "首次回答、同模型重试和模型切换共享上限。"],
        ["辅助调用", "任务分析和未命中缓存的视觉调用共享上限。"],
        ["总上游调用", "限制本次请求发出的全部物理调用。"],
        ["总截止时间", "分析、视觉、回答、重试和切换共享同一个截止时间。"],
        ["最坏费用", "按冻结计划控制最坏预计费用，不假设缓存一定命中。"],
      ]),
      noteSection("Session 锁定 Token 阈值", [
        "默认 100K Token，可由管理员在 Policy 中调整。",
        "锁定用于保护长会话提示缓存；达到稳定条件后，普通新任务优先保留已锁定模型。",
        "新用户任务仍重新分析风险；高风险、硬能力不满足或主动升级可以突破普通复用。",
      ]),
      elementSection("进一步阅读", [
        linkElement("先看名词解释", helpHref("glossary")),
      ]),
    ],
    ["打开 Profiles", "/_admin/profiles"],
  );
}

function visionHelp() {
  return helpPage(
    "视觉预处理",
    "视觉预处理只负责把图片转成文字上下文；最终回答仍由本次路由选中的回答模型完成。",
    [
      listSection("主要操作", [
        "启用视觉预处理，并从已录入模型中选择识图模型，或手动输入上游支持的模型 ID。",
        "在模型目录中准确标记回答模型是否原生支持视觉。",
        "调整客观识图提示词；相同图片和识图配置可复用识图缓存。",
      ], true),
      diagramSection(
        "vision-processing-flow.svg",
        "视觉预处理决策与调用链路",
        "回答模型原生支持图片时直接透传；否则先识图，再由原回答模型完成最终任务。",
      ),
      noteSection("执行边界", [
        "回答模型原生支持当前图片请求时直接透传，不发起辅助识图调用。",
        "回答模型不支持视觉时，系统先调用识图模型，再用识图文本替换图片后调用原回答模型。",
        "最终回答模型不会自动切成识图模型；视觉调用成功也不代表主回答一定成功。",
        "缓存命中只省去识图物理调用，主回答仍受统一预算和截止时间限制。",
      ]),
    ],
    ["打开 Profiles", "/_admin/profiles"],
  );
}

function reliabilityHelp() {
  return helpPage(
    "重试与超时",
    "重试不是看到失败就自动发生。必须同时满足错误可恢复、规则匹配、预算允许且客户端尚未收到响应。",
    [
      tableSection("同模型重试条件", ["条件", "说明"], [
        ["overload_rules 命中", "按顺序匹配状态码和可选响应正文；没有规则就没有同模型重试。"],
        ["Policy 仍有重试票据", "单 Upstream 重试上限只是上限，不能替代 overload_rules。"],
        ["共享截止时间未到", "分析、视觉和回答耗尽总时限后直接返回 504。"],
        ["ClientCommit 尚未发生", "响应一旦提交客户端，不再安全重试或切换模型。"],
      ]),
      diagramSection(
        "retry-timeout-state-machine.svg",
        "重试、超时与提交边界状态机",
        "错误可恢复、规则命中、预算、deadline 和 ClientCommit 边界必须同时允许。",
      ),
      noteSection("重试与模型切换", [
        "401、403 和未配置为可恢复的 4xx 不应通过重试掩盖。",
        "同模型重试用尽或不可用后，只有冻结执行计划包含候选且切换预算允许时才会换模型。",
        "本项目只连接一个 Upstream；节点负载均衡、健康检查和熔断由该网关负责。",
      ]),
      noteSection("排查 504", [
        "先查看共享总耗时和各物理调用耗时，再确认是否在进入重试判断前就已达到 deadline。",
        "路由轨迹中的 0 / network 表示没有有效 HTTP 状态；它仍需满足规则和预算才可能重试。",
      ]),
    ],
    ["打开 Profiles", "/_admin/profiles"],
  );
}

function agentsHelp() {
  return helpPage(
    "Agent 配置",
    "后台根据 Profile 协议生成 Claude Code、OpenCode、Codex 或通用客户端示例。",
    [
      listSection("主要操作", [
        "先保存 Profile，再选择客户端和配置作用域。",
        "填写代理地址和临时密钥占位符，下载或复制生成结果。",
        "使用 Profile 对应 URL，确保请求落入正确隔离边界。",
      ], true),
      noteSection("安全边界", [
        "生成器不会把输入的临时密钥保存到数据库。",
        "示例只生成连接和模型映射；调用路径与会话字段仍由客户端协议决定。",
      ]),
    ],
    ["选择 Profile", "/_admin/profiles"],
  );
}

function statisticsHelp() {
  return helpPage(
    "统计与排障",
    "用量统计、路由轨迹和模型表现分别回答用了多少、为什么这样选，以及模型实际表现如何。",
    [
      listSection("推荐排查顺序", [
        "先用会话 ID 或 request_trace_id 定位请求，确认最终状态和总耗时。",
        "查看任务分析、候选筛选、视觉处理、模型回答和请求失败分别在哪一步。",
        "展开候选排除原因和物理调用，核对实际模型、状态、重试、切换和费用。",
        "对比相邻请求，确认它们是同一会话的独立请求，不要把后续成功当成本次自动重试。",
      ], true),
      tableSection("常见信号", ["信号", "含义"], [
        ["0 / network", "上游调用没有取得有效 HTTP 状态，常见于连接中断或 deadline。"],
        ["504", "代理共享截止时间已耗尽；检查分析、视觉和回答累计耗时。"],
        ["没有视觉物理调用", "可能是原生视觉透传、未启用视觉，或识图缓存命中。"],
        ["候选被排除", "查看能力、Production 资格、质量、稳定性和严重错误率门槛。"],
      ]),
      noteSection("安全 Debug 日志", [
        "以 LOG_LEVEL=debug 启动服务，用 request_trace_id 串联 classification、candidate、attempt_plan、call 和 completed。",
        "Debug 仅记录结构化判断事实和费用，不记录请求正文、图片、鉴权信息或完整回复。",
      ]),
    ],
    ["打开统计", "/_admin/stats"],
  );
}

function glossaryHelp() {
  return helpPage(
    "术语表",
    "这些词对应当前后台字段和路由轨迹，不代表模型的永久全局评分。",
    [
      tableSection("请求与隔离", ["术语", "含义"], [
        ["Profile", "协议、Upstream、模型目录、Policy、视觉和容错的永久隔离边界。"],
        ["显式模型", "调用方指定具体 model；不进入智能路由。"],
        ["model=auto", "由代理分析任务并选择实际回答模型。"],
        ["Session", "识别同一会话，用于分类复用、模型稳定和提示缓存保护。"],
      ]),
      tableSection("分类与候选", ["术语", "含义"], [
        ["任务类型", "例如 code、research 或 simple，用于选择 Route。"],
        ["难度", "easy、medium、hard 或 unknown。"],
        ["风险", "本轮意图和实际操作的安全等级；普通 Shell/Edit 历史不会天然判为高风险。"],
        ["分类置信度", "任务分析结果的可信程度；不足时按安全回退处理。"],
        ["Production 候选", "有生产资格并通过本次硬门槛后可以接收在线流量。"],
        ["Shadow 候选", "只积累证据，不接收在线回答流量。"],
      ]),
      tableSection("执行与费用", ["术语", "含义"], [
        ["统一尝试预算", "共同限制任务分析、视觉、回答、重试、切换、时间和费用。"],
        ["ClientCommit", "响应已开始发送给客户端；之后不能再安全换一次完整回答。"],
        ["物理调用", "真正发送到上游的一次请求，包含辅助调用和主回答调用。"],
        ["完整成本", "任务分析、视觉、回答、重试和切换产生的全部已知费用。"],
      ]),
    ],
  );
}

function helpPage(title, description, sections, action) {
  const page = element("div", "stack routing-help-page");
  const hero = element("section", "card stack routing-help-hero");
  hero.append(textElement("h2", title), textElement("p", description, "muted"));
  if (action) hero.append(linkElement(action[0], action[1], "button button-secondary"));
  page.append(hero, ...sections);
  return page;
}

function listSection(title, items, ordered = false) {
  const section = element("section", "card stack routing-help-section");
  section.append(textElement("h2", title), listElement(ordered ? "ol" : "ul", items));
  return section;
}

function noteSection(title, items) {
  return listSection(title, items);
}

function elementSection(title, children) {
  const section = element("section", "card stack routing-help-section");
  section.append(textElement("h2", title), ...children);
  return section;
}

function tableSection(title, headings, rows) {
  const section = element("section", "card stack routing-help-section");
  const wrapper = element("div", "table-wrap");
  const table = element("table");
  const head = element("thead");
  const headRow = element("tr");
  for (const heading of headings) headRow.append(textElement("th", heading));
  head.append(headRow);
  const body = element("tbody");
  for (const row of rows) {
    const tableRow = element("tr");
    for (const value of row) tableRow.append(textElement("td", value));
    body.append(tableRow);
  }
  table.append(head, body);
  wrapper.append(table);
  section.append(textElement("h2", title), wrapper);
  return section;
}

function diagramSection(filename, alt, caption) {
  const section = element("section", "card stack routing-help-section routing-help-diagram");
  const figure = element("figure", "stack");
  const image = element("img");
  image.setAttribute("src", `/_admin/assets/current/${filename}`);
  image.setAttribute("alt", alt);
  image.setAttribute("loading", "lazy");
  image.setAttribute("decoding", "async");
  figure.append(image, textElement("figcaption", caption, "muted"));
  section.append(figure);
  return section;
}

function listElement(tagName, items) {
  const list = element(tagName, "stack help-topic-list");
  for (const item of items) list.append(textElement("li", item));
  return list;
}

function linkElement(text, href, className = "") {
  const link = textElement("a", text, className);
  link.setAttribute("href", href);
  return link;
}

function textElement(tagName, text, className = "") {
  const value = element(tagName, className);
  value.textContent = String(text);
  return value;
}

function element(tagName, className = "") {
  const value = document.createElement(tagName);
  value.className = className;
  return value;
}
