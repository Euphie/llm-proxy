import { helpHref } from "./routes.js";

const helpTopics = [
  ["overview", "使用概览"],
  ["glossary", "名词解释"],
  ["profiles-models", "Profiles 与模型"],
  ["agents", "Agent 配置"],
  ["vision", "视觉增强"],
  ["intelligent-routing", "智能路由"],
  ["reliability", "容错与上游节点"],
  ["statistics", "统计与诊断"],
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
  case "overview":
    return overviewHelp();
  case "glossary":
    return glossaryHelp();
  case "profiles-models":
    return simpleHelpTopic({
      title: "Profiles 与模型",
      description: "Profile 是协议、上游、模型事实和运行策略的隔离边界。普通转发只需要连接信息；视觉增强和智能路由才依赖完整模型目录。",
      steps: [
        "先创建 Profile，选择协议并填写上游地址；代理不会保存或管理上游密钥。",
        "需要视觉、智能路由或 Agent 上下文参数时，再录入精确且区分大小写的模型 ID。",
        "为模型补齐上下文、最大输出、能力和价格；自动模板只是辅助填写，保存值才是运行时事实。",
      ],
      notes: [
        "只做显式模型透传时，模型目录可以为空。",
        "模型被视觉、Route、模型角色或上游节点引用后，必须先解除引用才能删除或改名。",
      ],
      action: ["打开 Profiles", "/_admin/profiles"],
    });
  case "agents":
    return simpleHelpTopic({
      title: "Agent 配置",
      description: "后台根据 Profile 协议生成 Claude Code、OpenCode、Codex 或通用客户端示例，只生成连接与模型映射，不写入真实密钥。",
      steps: [
        "先保存 Profile，再从该 Profile 的“Agent 配置”页面选择客户端。",
        "填写代理公网地址和临时密钥占位符，按项目级或全局级下载配置。",
        "已录入模型上下文时，可填写压缩百分比并生成保守的上下文设置。",
      ],
      notes: [
        "生成器不会把输入的临时密钥保存到数据库。",
        "调用方最终发送的路径仍由 Agent 协议决定，Profile URL 只负责选择隔离配置。",
      ],
      action: ["选择 Profile", "/_admin/profiles"],
    });
  case "vision":
    return simpleHelpTopic({
      title: "视觉增强",
      description: "视觉增强用于主模型不支持图片的场景：代理先用视觉模型生成描述，再用描述替换图片后调用原主模型。",
      steps: [
        "启用视觉增强并选择视觉模型；未开启或模型为空时不会发送影子识图请求。",
        "在模型目录中标明哪些主模型支持视觉；不支持时才增强，支持时直接透传图片。",
        "按需调整识图提示词；相同图片和识图配置可以复用缓存，避免重复调用。",
      ],
      notes: [
        "视觉描述会影响主模型看到的图片信息，因此提示词应保持客观、完整，不直接替用户作答。",
        "缓存命中只省去视觉调用，不代表主回答请求也命中上游提示缓存。",
      ],
      action: ["选择 Profile", "/_admin/profiles"],
    });
  case "intelligent-routing":
    return intelligentRoutingHelp();
  case "reliability":
    return simpleHelpTopic({
      title: "容错与上游节点",
      description: "普通重试和上游节点切换是两层不同能力：重试规则匹配可恢复错误，备用上游节点只服务于 model=auto 的同模型服务切换。",
      steps: [
        "按优先级配置过载规则；状态码和可选正文同时匹配时，第一条规则生效。",
        "需要同模型多服务地址容错时，在智能路由启用后配置备用上游节点，并保持协议、供应商和凭据范围一致。",
        "用统一尝试预算限制重试、上游节点切换、模型切换、总调用次数和总截止时间。",
      ],
      notes: [
        "401、403 和不可重试 4xx 属于硬失败，不应通过重试掩盖。",
        "上游节点耗尽后只有执行计划允许时才切换模型；所有动作仍受统一预算限制。",
      ],
      action: ["打开 Profiles", "/_admin/profiles"],
    });
  case "statistics":
    return simpleHelpTopic({
      title: "统计与诊断",
      description: "统计分为用量统计、路由轨迹和模型表现，分别回答用了多少、为什么这样选、参与模型实际表现如何。",
      steps: [
        "用量统计按 Profile、协议、模型和时间查看 Token 与缓存用量。",
        "路由轨迹按正常、高风险、分析回退、升级/切换、失败和费用异常分类，并可展开分类依据、候选排除原因和物理调用链。",
        "模型表现按任务类型、难度、风险和视觉方式查看五维质量、可靠样本、成本及主动升级效果。",
        "排查慢请求时先比较视觉改写耗时与最终上游主回答耗时，缓存命中不代表主回答会变快。",
        "需要查看选模细节时以 LOG_LEVEL=debug 启动服务，按 request_trace_id 串联 routing.debug.classification、candidate、attempt_plan、budget_plan、call 和 completed。",
      ],
      notes: [
        "只有所有物理调用都返回完整 usage 且价格已配置时，实际费用才是完整已知。",
        "持久统计不保存原始提示词、图片、凭据或完整回答。",
        "Debug 日志只记录结构化判断事实与费用，不记录请求正文、图片、鉴权信息或模型完整回复。",
      ],
      action: ["打开统计", "/_admin/stats"],
    });
  default:
    return overviewHelp();
  }
}

function overviewHelp() {
  const page = element("div", "stack routing-help-page");
  const hero = element("section", "card stack routing-help-hero");
  hero.append(
    textElement("h2", "帮助中心"),
    textElement("p", "按照实际配置顺序查找说明。智能路由只是其中一个主题，其他功能可以独立使用。", "muted"),
  );
  const order = element("section", "card stack routing-help-section");
  order.append(
    textElement("h2", "推荐配置顺序"),
    orderedList([
      "创建 Profile 并完成连接配置。",
      "按需录入模型；只做普通转发时可以跳过。",
      "生成 Agent 配置并验证第一条请求。",
      "按需启用视觉增强、智能路由和上游节点容错。",
      "通过统计与路由轨迹检查效果和成本。",
    ]),
  );
  const topics = element("section", "help-topic-grid");
  for (const [id, label] of helpTopics.slice(1)) {
    const card = element("article", "card stack help-topic-card");
    card.append(
      textElement("h3", label),
      textElement("p", topicSummary(id), "muted"),
      linkElement("查看说明", helpHref(id)),
    );
    topics.append(card);
  }
  page.append(hero, order, topics);
  return page;
}

function topicSummary(topic) {
  return {
    glossary: "用简单例子理解 Profile、Route、上游节点、强模型基线等术语。",
    "profiles-models": "配置隔离边界、模型能力、上下文和价格。",
    agents: "生成 Claude Code、OpenCode、Codex 等客户端示例。",
    vision: "为不支持图片的主模型补充视觉理解。",
    "intelligent-routing": "理解 Route、质量门槛、成本选择和策略发布。",
    reliability: "配置普通重试、备用上游节点和统一尝试预算。",
    statistics: "查看用量、费用、视觉耗时和智能路由轨迹。",
  }[topic];
}

function glossaryHelp() {
  const page = element("div", "stack routing-help-page");
  const hero = element("section", "card stack routing-help-hero");
  hero.append(
    textElement("h2", "名词解释"),
    textElement(
      "p",
      "这里解释后台和日志中反复出现的词。可以先理解中文含义，再进入对应功能配置。",
      "muted",
    ),
  );
  page.append(
    hero,
    glossarySection("基础配置", [
      ["Profile", "一套彼此隔离的代理配置。", "例如 /crs 和 /jdcloud 可以连接不同上游并使用不同规则。"],
      ["Upstream", "Profile 默认连接的上游模型服务地址。", "显式模型请求通常直接转发到这里。"],
      ["协议", "调用方和上游使用的 API 格式。", "Anthropic Messages、OpenAI Chat Completions 或 Responses。"],
      ["模型 ID", "请求中 model 字段的原始值。", "claude-glm-5.2 会原样发送，不会被自动改成 GLM-5.2。"],
      ["模型目录", "当前 Profile 已登记的模型事实。", "记录上下文、视觉、工具、结构化输出和价格，不保存模型或密钥。"],
      ["Agent 配置", "供客户端连接代理的配置示例。", "可为 Claude Code、OpenCode 或 Codex 生成，但不会保存填写的密钥。"],
    ]),
    glossarySection("视觉增强", [
      ["主模型", "最终回答用户问题的模型。", "主模型不支持图片时仍可以借助视觉增强。"],
      ["视觉模型", "专门把图片转换为客观文字描述的模型。", "它负责看图，不负责代替主模型完成最终回答。"],
      ["视觉增强", "先识图，再把描述交给原主模型回答。", "它补充主模型的视觉能力，并不等于直接升级主模型。"],
      ["视觉缓存", "复用同一图片和同一识图配置产生的描述。", "命中后不再调用视觉模型，但主回答仍需正常请求上游。"],
    ]),
    glossarySection("智能路由", [
      ["model=auto", "让代理代替调用方选择实际模型。", "调用方指定具体模型时不会进入智能路由。"],
      ["参与模型", "管理员明确允许智能路由选择的模型。", "新录入模型不会自动加入，避免意外产生费用。"],
      ["任务分析模型", "在本地规则无法确定时判断任务类型、难度和风险的轻量模型。", "例如把请求识别为 simple、coding 或 long_context。"],
	  ["任务类型", "请求要完成的工作类别。", "例如 code、research 或 simple；用于映射 Route。"],
	  ["难度", "当前任务预计需要的推理强度。", "easy、medium、hard 或 unknown；可与任务类型一起映射更具体的 Route。"],
	  ["风险", "当前请求是否涉及明确敏感操作。", "普通 Shell/Edit 和历史工具调用不会天然成为高风险；分析失败显示为未知。"],
	  ["分类置信度", "任务分析结果的可信程度。", "置信度不足时不强行采用，按安全回退规则处理。"],
	  ["分析回退", "任务分析失败或置信度不足时使用强模型基线。", "它不等于高风险，也不会产生动态评测样本。"],
      ["强模型基线", "判断失败、高风险或普通候选都不合格时使用的可靠兜底模型。", "它是质量参照和安全下限，不代表每次请求都调用。"],
      ["Route", "一类任务的选模规则集合。", "coding Route 可以有自己的候选模型、质量门槛和错误率上限。"],
      ["候选模型", "某个 Route 允许比较的参与模型。", "候选先过能力和质量门槛，再比较完整成本。"],
      ["硬能力", "请求必须具备、不能用价格补偿的能力。", "工具调用或结构化输出不满足时，模型直接被排除。"],
      ["质量估计", "某模型在某个 Route 下完成任务的当前能力估计。", "92% 只表示该 Route 下的估计，不是永久的模型总分。"],
      ["严重错误率", "可能造成任务失败或错误执行的结果比例。", "超过 Route 上限时，即使价格更低也不会被选择。"],
      ["策略", "Route、候选、门槛和预算的完整版本。", "发布新策略不会原地改写正在使用的旧版本。"],
      ["Session 固定模型", "同一会话优先继续使用已经成功的模型。", "减少回答风格和缓存前缀频繁变化；能力升级时仍可换模型。"],
      ["模型主动升级", "回答模型明确发现能力不足时，请求代理切换到更强模型。", "它受工具能力、模型切换次数和总预算限制。"],
      ["动态策略优化", "异步比较候选与强基线并积累证据。", "不影响当前回答，也不会自动发布，只生成供管理员审查的新草稿。"],
      ["贝叶斯保守估计", "把管理员先验和异步评测证据合并成可信区间。", "质量使用保守下界，严重错误率使用保守上界；发布学习草稿后才影响在线路由。"],
    ]),
    glossarySection("容错与费用", [
      ["上游节点", "同一 Profile 内一个实际可调用的模型服务地址。", "主上游节点不可用时，可在同模型的兼容备用节点间切换。"],
      ["重试", "在可恢复错误发生后再次调用当前上游节点。", "401、403 和不可重试的 4xx 不会重试。"],
      ["模型切换", "主回答尚未提交客户端时，改用计划内的另一个模型。", "它不同于同模型的上游节点切换，通常也会改变缓存前缀。"],
      ["统一尝试预算", "一次请求允许消耗的所有调用和时间上限。", "共同限制任务分析、视觉、主回答、重试及各种切换。"],
      ["完整成本", "完成这次请求产生的全部模型费用。", "包括输入、输出、缓存读写、视觉、任务分析、重试和切换。"],
      ["路由轨迹", "系统为什么选择、重试或切换模型的过程记录。", "可在统计页查看，不保存原始提示词和完整回答。"],
    ]),
  );
  return page;
}

function glossarySection(title, rows) {
  return tableSection(title, ["名词", "简单理解", "例子或边界"], rows);
}

function simpleHelpTopic({ title, description, steps, notes, action }) {
  const page = element("div", "stack routing-help-page");
  const hero = element("section", "card stack routing-help-hero");
  hero.append(
    textElement("h2", title),
    textElement("p", description, "muted"),
    linkElement(action[0], action[1], "button button-secondary"),
  );
  const stepsSection = element("section", "card stack routing-help-section");
  stepsSection.append(textElement("h2", "主要操作"), orderedList(steps));
  const notesSection = element("section", "card stack routing-help-section");
  notesSection.append(textElement("h2", "关键边界"), unorderedList(notes));
  page.append(hero, stepsSection, notesSection);
  return page;
}

function intelligentRoutingHelp() {
  const page = element("div", "stack routing-help-page");
  page.append(
    hero(),
    conversationFlow(),
    diagramFigure(
      "智能路由整体架构",
      "/_admin/assets/current/intelligent-routing-architecture.svg",
      "智能路由控制面与请求面架构图：管理后台发布策略快照，Agent 请求经过 Profile、任务判断、Route 规划和统一预算后调用上游模型。",
      "控制面只负责配置、版本和热加载；请求面在固定 Profile 和策略快照内完成选模、视觉、预算与执行。实线是主流程，虚线是异步反馈，点线是配置或状态依赖。",
      "architecture",
    ),
    diagramFigure(
      "首次请求与后续追问流程",
      "/_admin/assets/current/intelligent-routing-flow.svg",
      "model auto 会话流程图：首次请求通过本地规则或轻量模型分析任务，后续追问复用 Session，并允许回答模型在输出正文前申请升级。",
      "左侧是首次请求，右侧是有效 Session 下的后续追问。两条路径都先完成每轮硬检查，再汇合到回答与主动升级流程；箭头只沿各自路径连接。",
      "flow",
    ),
    strategyFields(),
    selectionExample(),
    bayesianLearning(),
    diagramFigure(
      "异步评测与贝叶斯学习流程",
      "/_admin/assets/current/intelligent-routing-learning-flow.svg",
      "异步贝叶斯学习流程图：成功请求经过抽样、盲化对比、证据聚合和 Beta 后验更新，达到可靠样本后生成学习草稿，再由管理员决定是否发布。",
      "虚线是异步证据链，不阻塞当前回答；实线是管理员控制的发布链。贝叶斯结果只有进入新 active 策略后才影响未来请求。",
      "learning",
    ),
    budgetFields(),
    boundaries(),
  );
  return page;
}

function conversationFlow() {
  const section = element("section", "card stack routing-help-section");
  section.append(
    textElement("h2", "首次请求与后续追问"),
    textElement(
      "p",
      "只有 Agent 持续发送同一个有效的 X-LLM-Proxy-Session-ID，系统才能复用会话判断；没有有效 Session 时，每次都按首次请求处理。",
      "muted",
    ),
  );
  section.append(wrapTable(dataTable(
    ["阶段", "怎么判断", "如何升级"],
    [
      ["首次请求", "先运行本地零成本规则；无法确定时才调用轻量任务分析模型，然后映射 Route 并选模。", "选中的低级模型仍可在回答正文前调用内部升级工具，作为第二层保护。"],
      ["后续追问", "跳过轻量任务分析，沿用 Session 中的任务类型、Route 和模型；每轮仍检查风险与硬能力。", "当前模型发现任务明显变难时调用内部工具，代理用计划内更强模型重放原始请求。"],
    ],
  )));
  return section;
}

function hero() {
  const section = element("section", "card stack routing-help-hero");
  section.append(
    textElement("h2", "智能路由配置说明"),
    textElement(
      "p",
      "这份帮助对应 Profile 的“智能路由”页面。核心原则是：先过门槛，再比成本；价格不会补偿能力不足、质量不足或严重错误率过高。",
      "muted",
    ),
    linkElement("先看名词解释", helpHref("glossary")),
    linkElement("返回 Profiles", "/_admin/profiles", "button button-secondary"),
  );
  return section;
}

function diagramFigure(title, src, alt, caption, variant) {
  const section = element(
    "section",
    `card stack routing-help-section routing-help-diagram routing-help-diagram-${variant}`,
  );
  const figure = element("figure", "stack");
  const image = element("img");
  image.src = src;
  image.alt = alt;
  image.loading = "lazy";
  figure.append(image, textElement("figcaption", caption, "muted"));
  section.append(textElement("h2", title), figure);
  return section;
}

function strategyFields() {
  return tableSection(
    "策略与 Route 字段",
    ["字段", "怎么填写", "对路由的影响"],
    [
      ["策略名称", "唯一版本号，格式为 YYYYMMDD-NNN；保存后不原地修改。", "用于固定、发布和追踪策略版本。"],
      ["策略别名", "选填，例如“日常均衡”或“质量优先”。", "仅用于后台识别，不参与判断。"],
      ["默认 Route", "选择一个已添加的 Route。", "任务类型没有显式映射时使用。"],
      ["Route ID", "策略内唯一的内部标识，例如 simple、coding。", "供任务映射引用，不发送给上游。"],
      ["最低质量", "该类任务可以接受的最低质量百分比。", "候选质量低于它时直接排除。"],
      ["最大严重错误率", "该类任务可以接受的严重错误率上限。", "候选超过它时直接排除，价格不能抵消。"],
      ["候选模型", "从当前 Profile 已勾选的参与模型中选择。", "只在当前 Route 中参与选型。"],
      ["当前质量估计", "模型在当前 Route 下的初始质量事实。", "与最低质量直接比较；不是模型全局评分。"],
      ["当前严重错误率", "模型在当前 Route 下的初始严重错误事实。", "与最大严重错误率直接比较。"],
      ["任务类型", "必须与任务分析模型返回的分类完全一致。", "通过任务映射选择 Route。"],
      ["任务映射 Route", "为一个任务类型选择 Route。", "决定该类任务使用哪些候选和门槛。"],
    ],
  );
}

function selectionExample() {
  const section = element("section", "card stack routing-help-section");
  section.append(
    textElement("h2", "为什么候选会被排除"),
    textElement(
      "p",
      "假设 simple Route 的最低质量是 90%，最大严重错误率是 1%。",
      "muted",
    ),
  );
  const layout = element("div", "routing-help-example");
  const table = dataTable(
    ["候选", "质量", "严重错误率", "结果"],
    [
      ["fast", "92%", "0.5%", "通过门槛，参与成本比较"],
      ["strong", "99%", "0.1%", "通过门槛，参与成本比较"],
      ["cheap", "89%", "0.2%", "质量不足，排除"],
      ["unstable", "95%", "1.2%", "严重错误率过高，排除"],
    ],
  );
  const explanation = element("div", "stack routing-help-note");
  explanation.append(
    textElement("strong", "最终结果"),
    textElement("p", "只有 fast 和 strong 进入价格比较，系统选择预计完整成本更低者。"),
    textElement("p", "如果所有普通候选都被排除，只尝试满足硬能力要求的强模型基线；强基线也不满足时明确失败。"),
  );
  layout.append(wrapTable(table), explanation);
  section.append(layout);
  return section;
}

function bayesianLearning() {
  const section = element("section", "card stack routing-help-section");
  section.append(
    textElement("h2", "贝叶斯算法在哪生效"),
    textElement(
      "p",
      "贝叶斯更新不在当前请求中选模型。它只把异步盲评证据转换为质量保守下界和严重错误率保守上界；管理员生成、灰度并发布学习草稿后，未来请求才使用这些指标。",
      "muted",
    ),
    orderedList([
      "管理员填写的质量和严重错误率作为 20 个等效样本的 Beta 先验。",
	  "评审分别返回正确性、完整性、指令遵循、格式与工具安全、任务完成度五个维度的胜、负或平局。",
	  "五个维度分别按 30 天半衰期累计，再按 40%、20%、20%、10%、10% 汇总。",
      "系统计算单侧 95% 保守区间；五维有效样本达到 20 后才允许生成学习草稿。",
      "active 策略不会被后台证据直接修改，也不使用在线 Bandit 探索模型。",
    ]),
  );
  return section;
}

function budgetFields() {
  return tableSection(
    "统一尝试预算",
    ["字段", "限制什么", "建议理解"],
    [
      ["主回答尝试上限", "首次回答、回答重试和模型切换的总次数。", "限制最终回答链路，默认 2。"],
      ["辅助调用上限", "任务分析和实际未命中缓存的视觉调用总数。", "限制回答前的辅助模型调用，默认 2。"],
      ["总上游调用上限", "当前请求发出的全部上游调用。", "同时约束主回答与辅助调用，默认 5。"],
      ["单节点重试上限", "同一上游节点发生可重试故障后的追加次数。", "不会绕过总上游调用上限。"],
      ["上游节点切换上限", "同一模型最多切换多少个备用上游节点。", "0 表示不切换。"],
      ["模型切换上限", "回答提交客户端前最多切换多少次主模型。", "模型主动升级至少需要 1。"],
      ["请求总截止时间", "任务分析、视觉、回答、重试共享的总时限。", "例如 2m；不是每次调用各有 2m。"],
      ["最坏费用上限", "冻结执行计划时允许的最坏预计费用。", "0 表示不限制；不会假设缓存一定命中。"],
    ],
  );
}

function boundaries() {
  const section = element("section", "card stack routing-help-section");
  section.append(textElement("h2", "容易误解的边界"));
  const list = element("ul", "stack");
  for (const text of [
    "质量和严重错误率描述的是“某模型在某个 Route 下”的表现，不是一个永久的全局模型分数。",
    "动态策略优化不会直接修改 active 策略；它只能根据异步评测证据生成新的策略草稿。",
    "高风险请求优先使用强模型基线，不会因为低成本候选的配置分数较高而降级。",
    "显式 model 请求不进入智能路由；只有 model=auto 才读取这些配置。",
    "Session 会优先保持同一 Route 已成功的模型，但能力或质量要求升级时仍可切换。",
    "轻量任务分析主要服务首次请求；后续追问依靠 Session、每轮硬检查和模型主动升级工具。",
	"会话历史里的 shell_exec 或 edit 只说明之前做过什么；只有本轮最新文本或强制操作明确命中敏感规则时才进入高风险。",
  ]) {
    list.append(textElement("li", text));
  }
  section.append(list);
  return section;
}

function tableSection(title, headings, rows) {
  const section = element("section", "card stack routing-help-section");
  section.append(textElement("h2", title), wrapTable(dataTable(headings, rows)));
  return section;
}

function dataTable(headings, rows) {
  const table = element("table");
  const head = element("thead");
  const headRow = element("tr");
  for (const heading of headings) headRow.append(textElement("th", heading));
  head.append(headRow);
  const body = element("tbody");
  for (const row of rows) {
    const tableRow = element("tr");
    for (const cell of row) tableRow.append(textElement("td", cell));
    body.append(tableRow);
  }
  table.append(head, body);
  return table;
}

function wrapTable(table) {
  const wrapper = element("div", "table-wrap");
  wrapper.append(table);
  return wrapper;
}

function orderedList(items) {
  return listElement("ol", items);
}

function unorderedList(items) {
  return listElement("ul", items);
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
