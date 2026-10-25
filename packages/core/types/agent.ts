import type { ChatSession } from "./chat";

// ===========================================================================
// 文件职责：Agent（智能体）相关的全部线上数据结构，纯类型声明、无逻辑。
//
// 这里的"Agent"是产品的核心实体：一个绑定到某个运行时（runtime，如本地
// claude/codex CLI 或云端）的数字员工，能被 @提及 / 分配任务 / 群聊中调用，
// 在 issue、autopilot、chat 等场景里干活。文件回答了"一个 agent 在系统里
// 长什么样、能被谁调用、一次任务跑下来留下哪些记录、用量怎么计费"。
//
// 主要分组（按出现顺序）：
//   1. 状态/权限基础类型：AgentStatus、RuntimeVisibility 等简短枚举。
//   2. 调用权限模型（MUL-3963）：permission_mode + invocation_targets 决定
//      "谁能触发/分配/@ 这个 agent"，旧的 visibility 字段已退化为派生值。
//   3. 运行时与自定义运行时档案（RuntimeDevice / RuntimeProfile）：机器侧
//      注册的 daemon 实例 vs 工作区里手工创建的自定义运行时"配方"。
//   4. Agent 任务（AgentTask / TaskUsage / TaskAttribution）：任务从排队到
//      完成/失败的全状态机、失败原因、token 用量、追责人来源。
//   5. Agent 实体本体（Agent）：配置、指令、可见性、MCP/Composio 外接能力。
//   6. 增删改请求体与草稿（Create/UpdateAgentRequest、AgentBuilderSession）。
//   7. Skills：agent 可挂载的技能及其摘要/文件结构。
//   8. 用量与计费（RuntimeUsage / Dashboard*）：运行时、issue、工作区三个
//      层级的 token/成本统计，字段故意保持同构以便共用一个计价函数。
//   9. 运行时本地技能 / 模型目录（RuntimeLocalSkill* / RuntimeModel*）：
//      daemon 探测到的本地技能与可用模型清单（含 thinking level、服务档位）。
//
// 横切约定：本文件几乎所有"缺失即保守"的语义都写进各字段注释，读取侧务必
// 把 undefined / 缺失字段当作"后端版本较旧、按最保守方式处理"，而不是当 0
// 或 false。所有接口与后端 JSON 字段一一对应（snake_case）。
// ===========================================================================

/** Agent 当前生命状态：空闲 / 工作中 / 被阻塞 / 出错 / 离线。 */
export type AgentStatus = "idle" | "working" | "blocked" | "error" | "offline";

/** Agent 执行环境：跑在用户本机（local）还是 Multica 云端（cloud）。 */
export type AgentRuntimeMode = "local" | "cloud";

/**
 * Agent 可见范围（旧模型）：仅自己可见（private）还是整个工作区可见
 * （workspace）。注意：这是历史遗留字段，新逻辑看 permission_mode。
 */
export type AgentVisibility = "workspace" | "private";

// ---------------------------------------------------------------------------
// Agent 调用权限（MUL-3963）
//
// `permission_mode` + `invocation_targets` 才是决定"谁能触发 / 分配 / @ /
// 私聊一个 agent"的权威闸门。旧的 `visibility` 字段仍然保留，但后端已经
// 从这两个新字段"推导"出它：带 workspace 目标的 `public_to` agent 会被
// 推导成 `visibility: "workspace"`；其余情况（private，或 public_to 只圈
// 了 member/team 目标）一律推导成 `visibility: "private"`。
//
// 调用规则逐条解释：
//   - owner（agent 的创建者/拥有者）：任何模式下都能调用。
//   - permission_mode = "private"：只有 owner 能调用——工作区管理员也不
//     再能绕过（这是相比旧 visibility 模型最关键的行为变化）。
//   - permission_mode = "public_to" + workspace 目标：所有工作区成员可用。
//   - permission_mode = "public_to" + member 目标：仅被圈中的那个用户可用。
//   - team 目标：v1 预留、暂不生效（永远不会授予权限）。
// ---------------------------------------------------------------------------

/** Agent 的调用权限模式：仅 owner 可用（private）或面向指定对象开放（public_to）。 */
export type AgentPermissionMode = "private" | "public_to";

/**
 * 一条针对 agent 的"调用授权"记录：允许谁去调用它。
 * 目标为整个工作区时 `target_id` 是 `null`（即授权覆盖所有成员）；
 * 目标限定到某成员/某团队时，`target_id` 存对应的 id。
 */
export interface AgentInvocationTarget {
  /** 授权对象类型：整个工作区 / 单个成员 / 单个团队。 */
  target_type: "workspace" | "member" | "team";
  /** 被授权对象 id；workspace 目标为 null，member/team 目标必填。 */
  target_id: string | null;
}

/**
 * 创建/更新 agent 时提交的调用目标结构（请求体形态）。
 * workspace 目标可省略 `target_id`（后端自动补工作区 id）；
 * member / team 目标则必须带上 id。
 */
export interface AgentInvocationTargetInput {
  target_type: "workspace" | "member" | "team";
  target_id?: string;
}

// Runtime 的可见性是和 agent 可见性正交的另一条轴——用词不同，因为它守的是
// 另一扇门："谁能把 agent 绑到这个 runtime 上"。默认 "private"：只有 runtime
// 拥有者能绑定 agent（工作区管理员也不行），而且只有拥有者本人能改这个开关
// （MUL-6126）；"public" 则放开到任意工作区成员都能绑定。老后端还没发 MUL-2062，
// 会干脆缺省这个字段，读取方必须自行按 "private"（最严格）兜底。
export type RuntimeVisibility = "private" | "public";

/**
 * 一个运行时设备（RuntimeDevice）＝一台向后端注册过、可承载 agent 执行的机器
 * 实例（本地 daemon 或云端），是真正"干活的地方"。Agent 必须绑到某个 runtime
 * 才能跑任务。
 */
export interface RuntimeDevice {
  /** 运行时唯一 id。 */
  id: string;
  /** 所属工作区 id。 */
  workspace_id: string;
  /** 负责这台运行时的 daemon 进程 id；无 daemon（如纯云端）时为 null。 */
  daemon_id: string | null;
  /** 运行时名字（daemon 上报的原始名）。 */
  name: string;
  /**
   * 用户自定义的展示名（MUL-4217），有值时代替 `name` 显示。daemon 永远不会
   * 写这个字段，所以它能跨心跳存活。老后端会缺省它——读取方必须把"缺失/空"
   * 当作"用 name"处理（参见 runtimeDisplayName）。
   */
  custom_name?: string | null;
  /** 执行模式：本地 daemon 还是云端。 */
  runtime_mode: AgentRuntimeMode;
  /** 运行时所属的协议族/厂商，如 claude、codebuddy、codex。 */
  provider: string;
  /** 启动该运行时所使用的命令行头部（launch header）。 */
  launch_header: string;
  /** 在线状态：online / offline。 */
  status: "online" | "offline";
  /** 设备信息描述串（如系统版本、芯片等）。 */
  device_info: string;
  /** 扩展元数据，任意键值。 */
  metadata: Record<string, unknown>;
  /** 运行时的拥有者用户 id；平台级/共享运行时可能为 null。 */
  owner_id: string | null;
  /** 可见性开关；后端早于该特性时缺省，默认按 "private" 处理。 */
  visibility: RuntimeVisibility;
  /**
   * 该运行时从哪个自定义运行时档案（RuntimeProfile）启动，`null` 表示来自
   * 内置协议族。UI 据此给运行时行打 "内置 / 自定义" 徽标。老后端缺省该字段，
   * 读取方必须把缺失当作 `null`（内置）。
   */
  profile_id?: string | null;
  /** 最近一次心跳时间；从未上线则为 null。 */
  last_seen_at: string | null;
  created_at: string;
  updated_at: string;
}

/** AgentRuntime 是 RuntimeDevice 的别名，含义完全一致（强调"能跑 agent 的执行端"）。 */
export type AgentRuntime = RuntimeDevice;

// ---------------------------------------------------------------------------
// 自定义运行时档案（MUL-3284）
//
// RuntimeProfile 是工作区级别的"自定义运行时后端"定义，和 RuntimeDevice
// （daemon 注册出来的具体运行实例）是两码事：管理员先写一份档案（展示名 +
// 底层协议族 + 启动它的 CLI 命令），之后 daemon 可以照着这份档案去注册
// 具体运行时；那些实例会带 `profile_id` 指回这份档案。
// ---------------------------------------------------------------------------

// 自定义运行时允许包装的协议族白名单。创建流程只能在下面这些选项里挑，
// 服务器会拒绝白名单之外的值（400）。用 const 元组保存单一真源，
// 再用索引类型推导出联合类型，保证两者永远同步。
export const RUNTIME_PROFILE_PROTOCOL_FAMILIES = [
  "claude",
  "codebuddy",
  "codex",
  "copilot",
  "opencode",
  "codearts",
  "deveco",
  "hermes",
  "pi",
  "cursor",
  "kimi",
  "reasonix",
  "dsh",
  "dim",
  "kiro",
  "antigravity",
  "qoder",
  "qoderclicn",
  "traecli",
  "grok",
  "qwen",
  "qwenpaw",
  "mcode",
  "zeroclaw",
] as const;

/** 自定义运行时允许的协议族联合类型，从上面的白名单常量自动推导。 */
export type RuntimeProtocolFamily =
  (typeof RUNTIME_PROFILE_PROTOCOL_FAMILIES)[number];

// Profile 可见性沿用 RuntimeVisibility 的语义，但字段值用服务器文档所写的
// workspace/private 那一组词。
export type RuntimeProfileVisibility = "workspace" | "private";

/**
 * 一份"自定义运行时档案"：定义了一种可启动的自定义运行时后端长什么样
 * （如"用我家内网封装的 claude CLI 起一个运行时"），是创建运行时的配方。
 */
export interface RuntimeProfile {
  id: string;
  /** 所属工作区 id。 */
  workspace_id: string;
  /** 展示名。 */
  display_name: string;
  /** 底层协议族（必须在 RUNTIME_PROFILE_PROTOCOL_FAMILIES 白名单内）。 */
  protocol_family: RuntimeProtocolFamily;
  /** 启动命令名（CLI 可执行文件）。 */
  command_name: string;
  /** 一句话描述；可为 null。 */
  description: string | null;
  /** 追加给启动命令的固定参数列表。 */
  fixed_args: string[];
  /** 可见性：整个工作区可见还是仅创建者私有。 */
  visibility: RuntimeProfileVisibility;
  /** 创建者用户 id；系统内置档案可能为 null。 */
  created_by: string | null;
  /** 是否启用：停用的档案不能再用来起运行时。 */
  enabled: boolean;
  created_at: string;
  updated_at: string;
}

// POST 请求体。`protocol_family` 必填且创建后不可改。未设置的选填字段会整体
// 省略（绝不发 null / 空值），让服务器自行套用默认值。
export interface CreateRuntimeProfileRequest {
  display_name: string;
  protocol_family: RuntimeProtocolFamily;
  command_name: string;
  description?: string;
  fixed_args?: string[];
  visibility?: RuntimeProfileVisibility;
  enabled?: boolean;
}

// PATCH 请求体——所有字段都可选；`protocol_family` 刻意不在这里出现，
// 因为它是创建后不可变的。
export interface UpdateRuntimeProfileRequest {
  display_name?: string;
  command_name?: string;
  description?: string | null;
  fixed_args?: string[];
  visibility?: RuntimeProfileVisibility;
  enabled?: boolean;
}

// 后端在任务转为 "failed" 时打的粗粒度失败分类，对应迁移 055 里
// agent_task_queue.failure_reason 枚举，用于 agent 在线状态推导与 UI 的
// 失败文案查表。这是"粗"分类，线上还会出现更细的原因（见 AgentTask 注释）。
export type TaskFailureReason =
  | "agent_error" // agent 本身跑出错
  | "timeout" // 整体超时
  | "codex_semantic_inactivity" // codex 长时间无实质进展被判挂起
  | "runtime_access_revoked" // 运行时所有者撤销访问
  | "runtime_offline" // 运行时掉线
  | "runtime_reconnect_timeout" // 断线后重连超时
  | "runtime_recovery" // 运行时恢复过程失败
  | "manual"; // 人为终止

/**
 * Agents 列表"活动量"迷你柱状图里的一个数据桶（一天一个桶）。
 * 后端只返回至少完成过 1 个任务的日期；前端渲染 7 桶序列时把缺的
 * 天补 0。统计锚定在任务完成时间（completed_at）上——在途任务不计入。
 */
export interface AgentActivityBucket {
  agent_id: string;
  /** 该桶对应的天（UTC 零点那一瞬的 ISO 时间戳）。 */
  bucket_at: string;
  /** 当天完成的任务数。 */
  task_count: number;
  /** 当天失败的任务数（是 task_count 的子集）。 */
  failed_count: number;
  // task_count = completed_count + failed_count + cancelled_count; the
  // back-end always reports all three.
  completed_count: number;
  cancelled_count: number;
  // Absent on older servers; do not estimate duration from a task page.
  duration_ms?: number;
  duration_count?: number;
}

/** 单个 agent 近 30 天累计运行次数，驱动 Agents 列表的 RUNS 列。 */
export interface AgentRunCount {
  agent_id: string;
  run_count: number;
}

/**
 * GET /api/working-agents 返回的"正在干活 agent"摘要——只含对外安全展示的
 * 字段。该接口按工作区聚合：每个由用户创建、且至少有一个在跑任务、且符合
 * 筛选条件的 agent 恰好出现一次。
 */
export interface WorkspaceWorkingAgent {
  id: string;
  name: string;
  avatar_url: string | null;
  /** 该 agent 当前正在运行的任务数。 */
  running_task_count: number;
  /** 该 agent 在跑任务所关联的去重后 issue 列表（已套用接口的类型/范围/与我关系筛选）。 */
  issue_ids: string[];
}

/** 正在干活的 agent 所运行任务的来源类型：issue / 自动飞行(autopilot) / 聊天。 */
export type WorkspaceWorkingAgentType = "issue" | "autopilot" | "chat";

/** "与我相关"的过滤口径：我负责的 / 我创建的 / 我参与的 / 不限。 */
export type WorkspaceWorkingAgentMineRelation =
  | "assigned"
  | "created"
  | "involved"
  | "any";

/**
 * 从全局用户表解析出来的"用户引用"，对已离职成员也安全（不会因查无此人而
 * 出错）。`name` / `email` / `avatar_url` 在服务器补充完整前是缺省的（只在
 * 面向用户的任务展示面上才有）。渲染时要防御性兜底——只有 `id` 时退化成
 * 一个通用标签。见 MUL-4302 §9。
 */
export interface AttributionUser {
  id: string;
  name?: string;
  email?: string;
  avatar_url?: string;
}

/**
 * 指向"一次运行直接起因"的类型化句柄：用 kind 标明起因种类（如评论、
 * autopilot 运行…），ref_id 指向那个对象。
 */
export interface TaskEvidence {
  kind: string;
  ref_id: string;
}

/**
 * 一次 agent 运行最终追溯到"该由哪位真人负责"的归属结论（MUL-4302 §9）。
 * `source` 是自由文本（服务器可能加新档位），消费时要用 switch + default
 * 分支兜底。
 */
export interface TaskAttribution {
  /**
   * 追责的"水位线"级别，标识通过哪一层落到具体的真人：
   * `direct_human`(直接真人发起) | `delegation`(逐级下派) |
   * `comment_source`(由某条评论而来) | `rule_owner`(规则拥有者) |
   * `owner_fallback`(兜底到拥有者) | `backfill`(历史补录) |
   * `unattributed`(无法归因)。永不为空串。
   */
  source: string;
  /** 是否为可靠归因；owner_fallback / backfill / unattributed 等降级档为 false。 */
  precise: boolean;
  /** 责任发起人（"代 X 执行"的 X）。unattributed 时缺省。 */
  initiator?: AttributionUser;
  /** 授权人；autopilot 运行（rule_owner / owner_fallback）没有该字段。 */
  originator?: AttributionUser;
  /** 本次运行的直接起因，用于提供"跳转到证据"的交互入口。 */
  evidence?: TaskEvidence;
  /** 命中的规则版本 id（autopilot 场景）。 */
  rule_version_id?: string;
  /** 本任务是从哪个任务下派来的。 */
  delegated_from_task_id?: string;
  /** 本任务是对哪个任务的重试。 */
  retry_of_task_id?: string;
  /** 本任务是对哪个任务的重新运行。 */
  rerun_of_task_id?: string;
}

/**
 * AgentTask：一条交给 agent 执行的"任务"记录（一次干活调度的最小单元）。
 * 从创建起经历 queued → dispatched → running → 终态（completed/failed/
 * cancelled）的完整生命周期；任务既可来自 issue 评论，也可来自 chat 会话
 * 或 autopilot 规则。后端把任务放进队列，daemon（运行时所在机器）认领后
 * 才真正开工，各阶段时间戳与运行结果都记在这里。
 */
export interface AgentTask {
  id: string;
  /** 承接该任务的 agent id。 */
  agent_id: string;
  /** 实际执行该任务的运行时 id。 */
  runtime_id: string;
  /** 关联的 issue id。任务没有关联 issue 时为空串 ""（chat 或 autopilot
   *  产生的任务通常如此），此时靠 chat_session_id / autopilot_run_id 判断
   *  它到底从哪个源头而来。 */
  issue_id: string;
  /**
   * 任务当前状态：
   *   - queued：排队等 daemon 认领
   *   - dispatched：已派发出去
   *   - waiting_local_directory：daemon 发出的"本地目录占用等待"——任务已派发
   *     但被搁置，因为另一个任务正锁着同一个磁盘路径
   *   - running：执行中
   *   - completed / failed / cancelled：终态
   * 凡是要把任务划分成"活跃 vs 已结束"的消费方，都必须把
   * waiting_local_directory 与 queued/dispatched/running 一起算作活跃（非终态）。
   */
  status:
    | "queued"
    | "dispatched"
    | "waiting_local_directory"
    | "running"
    | "completed"
    | "failed"
    | "cancelled";
  /** 调度优先级（数值越大越优先）。 */
  priority: number;
  /** 冗余存放 agent 的 id 与名字，方便列表直接展示，不必再查 agent。 */
  agent?: {
    id: string;
    name: string;
  };
  /** 派发时间；尚未派发为 null。 */
  dispatched_at: string | null;
  /** 开始执行时间；尚未开工为 null。 */
  started_at: string | null;
  /** 完成时间；未到终态为 null。 */
  completed_at: string | null;
  /** 执行结果（自由结构，由执行体回填）。 */
  result: unknown;
  /** 失败时的错误文案；未失败为 null。 */
  error: string | null;
  /**
   * 失败原因（任务不在失败态时为空串或缺失，后端对非失败任务用 omitempty
   * 省略该字段）。线上是开放的字符串而不是封闭枚举：后端的失败分类法已远超
   * TaskFailureReason——自 MUL-1949 起细化了 21+ 个 `agent_error.*` 原因、
   * 还有 `local_directory_error` 等，并且还在增长，老版本客户端会遇到自己
   * 编译时尚不存在的分类。TaskFailureReason 保留在联合类型里是为了给粗分类
   * 提供自动补全；`string & {}` 则容纳其余一切而不会破坏提示。
   */
  failure_reason?: TaskFailureReason | (string & {}) | "";
  created_at: string;
  /** 当任务由某 chat 会话产生时非空。 */
  chat_session_id?: string;
  /** 当任务由某 autopilot 运行产生时非空。 */
  autopilot_run_id?: string;
  /** 本任务是某个父任务的自动重试时被设置。 */
  parent_task_id?: string;
  /** 从 1 开始计数的尝试次数；>1 说明这是重试。 */
  attempt?: number;
  /** 当某条 issue 评论触发本任务（@提及或负责人评论）时记录该评论 id。 */
  trigger_comment_id?: string;
  trigger_comment_created_at?: string;
  /**
   * 本任务被认领前并入的、更早的评论 id（把这些评论也一起交给 agent 处理）。
   * 注意不含 `trigger_comment_id`（它始终是本任务最新的触发点）。二者取并集
   * 就是"排队时的覆盖计划"；认领后的消费方应优先看 `delivered_comment_ids`
   * （若有那份回执）。老后端会缺省该字段，未合并过的运行也没有。
   */
  coalesced_comment_ids?: string[];
  /**
   * 任务最近一次"认领响应"里实际夹带处理的评论 id 列表。任务一旦离开 queued
   * 态，这份就是权威的覆盖回执。老后端缺省——消费方可退回用 trigger/coalesced
   * 的计划并集兜底；显式给一个空数组同样权威（表示确实一条都没带）。
   */
  delivered_comment_ids?: string[];
  /**
   * 触发本任务的来源做的简短描述快照，创建任务那一刻截下。评论触发的任务是
   * 评论正文（约截断 200 字符）；autopilot 触发的是其标题；直接分配和 chat
   * 任务为 NULL。即使来源评论 / autopilot 后来被改或删除，这份快照也在。
   */
  trigger_summary?: string;
  /**
   * 分配方在启动本次运行时的交接嘱咐（MUL-3375）。只在带备注的分配触发运行
   * 上出现；执行日志把它当触发原因内联展示。缺失（老版本 / 无备注）时退回到
   * 通用文案 "initial run"。
   */
  handoff_note?: string;
  /**
   * 服务器算好的来源判别标记：活动行用它给"没有关联 issue"的任务贴标签
   * （比如快速创建的任务能显示有意义的标题，而不是落入 "Untracked"）。
   */
  kind?: "comment" | "autopilot" | "chat" | "quick_create" | "direct";
  /**
   * daemon 为本次任务钉住的本地工作目录。在 daemon 上报 work_dir 前为空
   * （一般要等执行真正开始）。这是 agent 实际跑代码的权威绝对路径；UI 表面
   * 应优先用 `relative_work_dir`，避免把用户主目录路径泄漏出去。
   */
  work_dir?: string;
  /**
   * `work_dir` 的"隐私安全展示版"，由服务器推导。标准任务会剥掉 daemon 的
   * workspaces 根目录（剩 `<wsUUID>/<taskShort>/workdir`）；本地目录任务若
   * 路径不在那个布局里，服务器会先剥掉已识别的主目录前缀（`/Users/<name>/`、
   * `/home/<name>/`、`<drive>:/Users/<name>/`），都不行就退回文件名，确保
   * 主目录和用户名不泄漏进 UI。老后端缺省该字段——要有条件地渲染，且任何
   * 情况下都不要裸渲 `work_dir`（包括 tooltip / title / aria-label，因为截图
   * 与录屏也要保证安全）。
   */
  relative_work_dir?: string;
  /**
   * 在 daemon 确认一次性本地 worktree 已收尾并删除后，取代 `work_dir` 的
   * 持久目录。终态任务可用它做明确的剪贴板操作；缺省则 `work_dir` 仍是权威
   * （包括保留 worktree 失败的场景与老 daemon/服务器组合）。这是某一刻的
   * 交付快照；之后的资源改名或摘除不会回头改写历史任务。
   */
  durable_work_dir?: string;
  /**
   * `durable_work_dir` 的隐私安全展示版。永远不要直接渲染绝对 durable 路径；
   * 老后端两个字段都会缺省。
   */
  relative_durable_work_dir?: string;
  /**
   * 本次运行交付成果所在的 git 分支。只有 worktree 模式的本地目录任务会设置：
   * 这种模式下 agent 从不碰用户的干活副本，分支是找到它产物的唯一指针。
   *
   * 失败任务上也可能有：worktree 模式在拆掉 worktree 前会把 agent 留下的任何
   * 东西提交掉，所以半路挂掉的运行仍有值得找的东西。与 `work_dir` 不同，
   * 这个字段可以原样渲染——它是用户自己仓库里的一个 ref，不是文件系统路径。
   * 老后端缺省，要有条件地渲染。
   */
  branch_name?: string;
  /**
   * 本次运行解析出的"真人负责人"溯源（MUL-4302 §9）：代表谁跑的、怎么解析
   * 出来的、证据/血缘是什么。面向用户的任务展示面上有；老后端缺省——有条件
   * 地渲染。
   */
  attribution?: TaskAttribution;
  /**
   * 本次运行自身的 token 消耗明细，按其用过的 (provider, model) 逐条列出。
   * 只出现在 issue 执行日志接口；daemon 认领路径不会带。
   *
   * `undefined`（老后端，或没填充该数据的页面）和 `[]`（后端有填充，但本次
   * 运行没有记录用量）都表示"没数可展示"，必须渲染成破折号而不是 0——一次
   * 在用量上报上线前跑完的任务并不免费，只是我们不知道它花了多少。
   */
  usage?: TaskUsage[];
}

/**
 * 单次运行 token 用量在"某一 (provider, model)"切片下的明细。
 *
 * 字段名刻意与 {@link RuntimeUsage} 保持一致，这样 packages/views/runtimes/
 * utils.ts 里的 `estimateCost` / `estimateCostBreakdown` /
 * `estimateCacheSavings` 对"单次运行"和"运行时某天"用同一套计价函数——
 * 产品里永远只有一条成本公式。
 *
 * `cost_usd_ticks` 是 provider 自己对这一片的报价（1e-10 美元），provider 没
 * 报数时缺省，这些 token 改由客户端的价目表估算。和聚合行不同，这里没有
 * `uncosted_*` 拆分：一条 task_usage 记录要么有价要么没价，"没价"就等于
 * "cost_usd_ticks 缺省时的全部 token"，正是估算器已有的假设。
 */
export interface TaskUsage {
  /** 供应商标识（缺省时属未知供应商）。 */
  provider?: string;
  /** 模型名，如 claude-sonnet-4-5。 */
  model: string;
  /** 输入 token 数。 */
  input_tokens: number;
  /** 输出 token 数。 */
  output_tokens: number;
  /** 命中缓存的读取 token 数。 */
  cache_read_tokens: number;
  /** 写入缓存的 token 数。 */
  cache_write_tokens: number;
  /** 供应商自报成本（1e-10 美元）；缺省表示该用价目表估。 */
  cost_usd_ticks?: number;
}

/**
 * Mika 引导（bootstrap）接口的响应：工作区的 Mika agent 本身 + 调用者与它的
 * 会话，二者由服务器端一起解析返回——避免两个客户端各自开一个引导会话。
 */
export interface MikaBootstrapResponse extends Agent {
  /** 仅当服务器解析不到会话时缺省；此时应重试同一调用，而不是在客户端自建。 */
  onboarding_session?: ChatSession;
}

/**
 * Agent（智能体）实体本体：系统里一个"数字员工"的完整配置与运行绑定。
 * 包括它叫什么、提示词、绑在哪个 runtime、能被谁调用、挂了哪些技能与
 * 外接能力（MCP/Composio）。创建/编辑表单提交的是 Create/UpdateAgentRequest，
 * 读出来展示的基本就是本结构。
 */
export interface Agent {
  id: string;
  /** 所属工作区 id。 */
  workspace_id: string;
  /**
   * 绑定的运行时 id。agent 未绑定任何 runtime 时为空串：它的配置、聊天与
   * 任务历史仍在（runtime 被删时得以保留），只是要重新绑一个才能再跑
   * （MUL-5559）。判断"是否已绑定"请用 `isAgentRuntimeBound` 帮助函数，让
   * 新增信号与老版本信号兼容；也别把它和"绑了但离线"混淆——后者只需机器
   * 回来即可。
   */
  runtime_id: string;
  /** 恰在 agent 没有 runtime 时为 false。老后端缺省该字段。 */
  runtime_bound?: boolean;
  /** Privacy-safe coarse liveness for a runtime hidden from the runtime list. */
  runtime_availability?: "online" | "unstable" | "offline";
  name: string;
  /** 一句话描述。 */
  description: string;
  /** Agent 的完整可编辑提示词。 */
  instructions: string;
  /** 最多三条、由 agent 作者预设的首轮对话建议。老服务器缺省。 */
  conversation_starters?: AgentConversationStarter[];
  /** 产品定义的 agent（如 "mika"）才有。用户/模板创建的 agent 缺省。这是
   *  判断"由 Multica 维护"的身份键——别拿展示名判，因为名字能被拥有者改。 */
  system_key?: string;
  avatar_url: string | null;
  /** 执行模式：本地 daemon / 云端。 */
  runtime_mode: AgentRuntimeMode;
  /** 运行时的启动配置（透传给运行时，键值不透明）。 */
  runtime_config: Record<string, unknown>;
  /** 追加给运行时的自定义命令行参数。 */
  custom_args: string[];
  /**
   * 只表达"该 agent 是否配置了自定义环境变量"的粗信号，不暴露键或值。
   * 想读真实 env 映射要走专门的 `GET /api/agents/{id}/env` 接口（仅 agent
   * 拥有者或工作区 owner/admin，且留审计日志）。MUL-2600。
   *
   * 类型里标可选，让老后端（MUL-2600 之前）缺省该字段时不至于把渲染器弄崩；
   * 下游应把 `undefined` 当作"未知——按没有 env 处理"，而不是"确定有 env"。
   */
  has_custom_env?: boolean;
  /**
   * 该 agent custom_env 映射里的键数量，总是和 `has_custom_env` 一起出现。
   * `undefined` 按 0 处理。MUL-2600。
   */
  custom_env_key_count?: number;
  /**
   * 转发给"消费 agent.mcp_config 的运行时"的 MCP 服务器配置（是否支持见
   * providerSupportsMcpConfig）。各后端把它落到运行时的原生位置：Claude 的
   * flags、Codex 的 config.toml、ACP 会话参数、OpenCode 的 env 配置。`null`
   * （或老后端缺省该字段）表示没有托管配置，daemon 退回用 CLI 自带的默认值。
   * MUL-2764。
   *
   * 当调用方看不到密钥（agent 本体、或非 owner 非 admin 的用户）时，服务器
   * 会把值换成 `null` 并把 `mcp_config_redacted` 置 true，让 UI 能渲染出
   * "已配置但隐藏"的状态而不暴露敏感字段。
   */
  mcp_config?: unknown | null;
  /**
   * 服务器是否因为调用者无权看密钥而剥掉了 `mcp_config`。UI 用它区分"没有
   * 配置"（`mcp_config === null && !mcp_config_redacted`）和"有配置但你
   * 看不到"。老后端缺省该字段，`undefined` 按 false 处理。
   */
  mcp_config_redacted?: boolean;
  /**
   * 允许该 agent 在派发任务时挂成 MCP 服务器的 Composio 工具集 slug 子集
   * ——但仅在"运行发起人是 agent 拥有者"时才生效（MUL-3869 / MUL-3721）。
   * `null`/`[]`/缺省都表示"无论谁触发都不叠加"。拥有者专属数据：服务器原样
   * 给拥有者，对其他人脱敏成 `undefined` + `composio_toolkit_allowlist_
   * redacted=true`（契约同 `mcp_config`）。`undefined` 按"未知——假定没有"。
   */
  composio_toolkit_allowlist?: string[];
  /**
   * 服务器是否因调用者非 agent 拥有者而剥掉了 `composio_toolkit_allowlist`。
   * MCP 页签仅创建者可进，理论上脱敏值到不了编辑器，但 UI 仍防御性地渲染
   * "隐藏"兜底。老后端缺省该字段，`undefined` 按 false 处理。
   */
  composio_toolkit_allowlist_redacted?: boolean;
  visibility: AgentVisibility;
  /**
   * 权威的调用权限模式（MUL-3963）。上面的 `visibility` 字段由后端从它推导。
   * 当前后端总是返回该字段。
   */
  permission_mode: AgentPermissionMode;
  /** 支撑 `permission_mode === "public_to"` 的授权对象列表（private agent 为空）。 */
  invocation_targets: AgentInvocationTarget[];
  status: AgentStatus;
  /** 同一时刻最多并发跑几个任务。 */
  max_concurrent_tasks: number;
  /** 模型名。 */
  model: string;
  /**
   * 运行时的推理强度/努力度 token（如 Claude 的
   * `low|medium|high|xhigh|max`、Codex 的
   * `none|minimal|low|medium|high|xhigh|max|ultra`）。空串表示"不覆盖"：
   * 后端省略 effort 旗标，由上游 CLI 配置 / 内置默认在运行时决定。选择器按
   * runtime+model 给出——API 从不跨厂商归一化。老后端整个缺省该字段，
   * `undefined` 按 "" 处理（MUL-2339）。
   */
  thinking_level?: string;
  /** 运行时原生的 Codex 服务档位（如 `priority`，界面显示 Fast）。空/缺省
   *  表示不覆盖：本地 Codex 配置与账号默认值仍然权威。 */
  service_tier?: string;
  owner_id: string | null;
  /** agent 已挂载技能的摘要列表。 */
  skills: AgentSkillSummary[];
  /** 该 agent 明确"不继承"的运行时本地技能。老服务器缺省。 */
  disabled_runtime_skills?: DisabledRuntimeSkill[];
  created_at: string;
  updated_at: string;
  archived_at: string | null;
  archived_by: string | null;
}

/** 一条"开场白"建议：空聊天状态下给用户点选的一个小芯片。 */
export interface AgentConversationStarter {
  /** 芯片上显示的一句话标签。 */
  label: string;
  /** 被选中后填入输入框的完整可编辑文本。 */
  prompt: string;
}

/**
 * 一条被禁用的运行时本地技能：runtime 本地扫描出了一堆技能，agent 作者
 * 明确不让某个 agent 继承其中的某条。
 */
export interface DisabledRuntimeSkill {
  /** 这个禁用决策作用于哪个运行时。 */
  runtime_id: string;
  /** 该技能的提供方。 */
  provider: string;
  /** 技能来源根：provider 自带 / 跨工具通用 / 某个插件。 */
  root: "provider" | "universal" | "plugin";
  /** 技能的稳定键。 */
  key: string;
  /** 技能展示名（可选）。 */
  name?: string;
  /** 来源插件名（root 为 plugin 时才有）。 */
  plugin?: string;
}

/** 请求体：开启/关闭某条运行时本地技能对某个 agent 的可用性。 */
export interface SetAgentRuntimeSkillEnabledRequest {
  runtime_id: string;
  root: "provider" | "universal" | "plugin";
  key: string;
  name: string;
  plugin?: string;
  enabled: boolean;
}

/**
 * 内嵌在 Agent 载荷里的技能最小形态（`GET /api/agents`、`GET /api/agents/:id`
 * 返回它）。只有 id/name/description 有值——agent 列表的批量查询只 join 了
 * 这三列。要拿完整技能信息，用 `GET /api/agents/:id/skills`（返回
 * `SkillSummary[]`）或 `GET /api/skills/:id`（返回完整 `Skill`）。
 */
export interface AgentSkillSummary {
  id: string;
  name: string;
  description: string;
	/** 老服务器缺省该字段；消费方须把缺省当作"已启用"。 */
	enabled?: boolean;
}

/** 创建 agent 的请求体（POST /api/agents）。必填只有 name + runtime_id。 */
export interface CreateAgentRequest {
  name: string;
  description?: string;
  instructions?: string;
  conversation_starters?: AgentConversationStarter[];
  avatar_url?: string;
  runtime_id: string;
  runtime_config?: Record<string, unknown>;
  custom_env?: Record<string, string>;
  custom_args?: string[];
  visibility?: AgentVisibility;
  /**
   * 调用权限模式（MUL-3963）。有值即以它为准；缺省时后端按旧 `visibility`
   * 字段映射（private → private；workspace → public_to + workspace 目标）。
   * 更新时权限变更仅 OWNER 可改（非 owner 的管理员提交这些字段会被后端
   * 静默忽略）。
   */
  permission_mode?: AgentPermissionMode;
  /** 授权对象——见 `AgentInvocationTargetInput`。 */
  invocation_targets?: AgentInvocationTargetInput[];
  max_concurrent_tasks?: number;
  model?: string;
  /** 可选的运行时原生推理强度 token，见 `Agent.thinking_level`。 */
  thinking_level?: string;
  /** 可选的 Codex 服务档位目录 id，见 `Agent.service_tier`。 */
  service_tier?: string;
  /** 可选的创建来源标注。会以 `template` 属性出现在 `agent_created`
   *  PostHog 事件上。 */
  template?: string;
  /** 创建 agent 行时一起原子挂上的工作区技能 id。 */
  skill_ids?: string[];
}

/** 一次"用对话方式创建 agent"（builder）会话的载体信息：它在哪个会话、
 *  由哪个 builder agent、在哪个 runtime 上跑。 */
export interface AgentBuilderSession {
  session_id: string;
  builder_agent_id: string;
  runtime_id: string;
}

/** 创建表单里建模的"谁可以调用待创建 agent"：仅自己 / 整个工作区 / 指定成员。 */
export type AgentPermissionScope = "private" | "workspace" | "members";

/**
 * 一份"进行中 agent 配置"的落盘形态（builder 会话里可恢复的草稿）。
 *
 * 和可编辑草稿有两处刻意不同：`Set` 变成了数组（JSON 没有集合类型），以及
 * 不带 runtime——会话到底跑在哪个 runtime 由服务器侧承载它的 agent 说了算，
 * 在这里存一份副本只会过期。`applied_message_id` 一并保存，因为它能阻止
 * "恢复草稿"时把最后一条回复里的 `<agent_draft>` 再次套用到用户之后的手动
 * 修改上。
 */
export interface StoredAgentDraft {
  name: string;
  description: string;
  instructions: string;
  conversation_starters: AgentConversationStarter[];
  avatar_url: string | null;
  model: string;
  thinking_level: string;
  service_tier: string;
  skill_ids: string[];
  permission_scope: AgentPermissionScope;
  member_ids: string[];
  team_ids: string[];
  applied_message_id: string | null;
}

/** studio 列表里展示的一条"尚未创建完的 agent"会话摘要。 */
export interface AgentBuilderSessionSummary {
  session_id: string;
  title: string;
  /** 承载会话的 runtime——真正执行这条对话的地方。选择器以它作初始值，
   *  保证 UI 显示和"下一条消息谁来答"永远一致（MUL-5163）。 */
  runtime_id: string;
  created_at: string;
  updated_at: string;
  /** 最新一条消息内容，还是 builder 的线上格式——给真人看之前要先用
   *  builder 协议的解码工具处理。 */
  last_message_content: string;
  last_message_role: string;
  last_message_at: string;
  /** 已保存的配置；若该会话从未被手工编辑过则为 null——此时客户端重放
   *  最后一条 `<agent_draft>` 块。 */
  draft?: StoredAgentDraft | null;
}

/**
 * 把一条进行中的 builder 会话重新绑到另一 runtime 的结果。
 * `runtime_id` 是服务器实际绑定的那个——调用方必须等它返回后再把新 runtime
 * 显示为已选中。
 */
export interface AgentBuilderRuntimeSwitch {
  runtime_id: string;
}

/** 更新 agent 的请求体（PATCH /api/agents/:id）：所有字段都可选，传哪个改哪个。 */
export interface UpdateAgentRequest {
  name?: string;
  description?: string;
  instructions?: string;
  conversation_starters?: AgentConversationStarter[];
  avatar_url?: string;
  runtime_id?: string;
  runtime_config?: Record<string, unknown>;
  /**
   * 注意：`custom_env` 刻意不允许走这个请求体更新。env 的改动一律走
   * `client.updateAgentEnv` / `PUT /api/agents/{id}/env`——那条路径允许
   * agent 拥有者或工作区 owner/admin，拒绝 agent 本体，并写持久审计行。
   * 服务器会对任何带 `custom_env` 的 `PUT /api/agents/{id}` 请求体回 400；
   * 别把这个字段塞进本载荷。MUL-2600。
   */
  custom_args?: string[];
  /**
   * MCP 服务器配置。三态语义（MUL-2764）：
   *   - 字段整体省略 → 不修改
   *   - `null` → 清空该列；daemon 启动时退回 CLI 内置默认
   *   - 传对象 → 原样替换存储的 JSON；各运行时后端按各自的 MCP 集成
   *     校验/转换
   */
  mcp_config?: unknown | null;
  /**
   * Composio 工具集白名单。三态语义，镜像后端闸门（MUL-3869）：
   *   - 字段整体省略 → 不修改
   *   - `null` → 清空该列（对任何人都没有 MCP 叠加）
   *   - string[] → 整表替换；服务器落库前会做小写化 / 去空白 / 去重
   * 若调用者不是 agent 拥有者，写入会在服务端被静默丢弃，所以 UI 只在
   * 创建者可进的 MCP 页签暴露该字段。
   */
  composio_toolkit_allowlist?: string[] | null;
  visibility?: AgentVisibility;
  /**
   * 调用权限模式（MUL-3963）。有值即以它为准；缺省时后端按旧 `visibility`
   * 字段映射。更新时权限变更仅 OWNER 可改（非 owner 管理员提交会被静默忽略）。
   */
  permission_mode?: AgentPermissionMode;
  /** 授权对象——见 `AgentInvocationTargetInput`。 */
  invocation_targets?: AgentInvocationTargetInput[];
  status?: AgentStatus;
  max_concurrent_tasks?: number;
  model?: string;
  /**
   * 运行时原生推理强度 token。三态语义（MUL-2339）：
   *   - 字段整体省略 → 不修改
   *   - "" → 清空覆盖；后端省略 effort 旗标，由本地 CLI 配置 / 内置默认决定
   *   - 非空 → 设定；服务器会按目标 runtime 的 provider 枚举校验，不认识回 400
   */
  thinking_level?: string;
  /**
   * Codex 服务档位覆盖：省略保留原值，"" 清除，非空值存一条运行时目录 id。
   */
  service_tier?: string;
}

/**
 * 专用 env 管理接口（`GET /api/agents/{id}/env`、`PUT /api/agents/{id}/env`）
 * 的线上结构。刻意与 `Agent` 分开，让常规 agent 读取不可能意外把 env 值
 * 泄漏出来。MUL-2600。
 */
export interface AgentEnvResponse {
  agent_id: string;
  /** agent 的自定义环境变量映射。 */
  custom_env: Record<string, string>;
}

/**
 * `PUT /api/agents/{id}/env` 的请求体。值等于 `"****"` 的项会被服务器当作
 * "保留这个键的现值"——这是一道纵深防御：即使用 UI 把打了码的映射原样回传，
 * 也不会意外覆盖真实密钥。只提交表单里动过的键；没传的键会被服务器删除。
 */
export interface UpdateAgentEnvRequest {
  custom_env: Record<string, string>;
}

// Skills（技能）

/**
 * 列表接口（`GET /api/skills`、`GET /api/agents/:id/skills`）返回的轻量技能
 * 形态。刻意不含完整 SKILL.md `content`——单篇正文常有 50–200KB，塞进列表
 * 载荷会让高延迟链路下的 CLI 超时（GH multica-ai/multica#2174）。需要正文时
 * 走详情接口拿 `Skill`。内嵌在 `Agent` 载荷里的请见 `AgentSkillSummary`。
 */
export interface SkillSummary {
  id: string;
  workspace_id: string;
  name: string;
  description: string;
  /** 技能的附加配置（键值不透明）。 */
  config: Record<string, unknown>;
  created_by: string | null;
  created_at: string;
  updated_at: string;
	/** 仅从"按 agent 划分的指派接口"返回时会出现。 */
	enabled?: boolean;
}

/** 完整技能：在摘要基础上多了 SKILL.md 正文与附属文件列表。 */
export interface Skill extends SkillSummary {
  /** SKILL.md 的正文内容。 */
  content: string;
  /** 技能附带的参考/素材文件。 */
  files: SkillFile[];
}

/** 技能包里的一个附属文件：一段带路径的文本内容。 */
export interface SkillFile {
 kind?: "text" | "binary";
 content_type?: string;
 size_bytes?: number;
 sha256?: string;
 mode?: number;
  id: string;
  skill_id: string;
  /** 相对路径。 */
  path: string;
  /** 文件内容。 */
  content: string;
  created_at: string;
  updated_at: string;
}

/** 创建技能的请求体。 */
export interface CreateSkillRequest {
  name: string;
  description?: string;
  content?: string;
  config?: Record<string, unknown>;
  files?: { id?: string; path: string; content: string; kind?: "text" | "binary"; mode?: number }[];
}

/** 上传压缩包走 POST /api/skills/import 时的结构化返回。 */
export interface SkillImportResult {
  /** 每个文件的导入结局：新建 / 覆盖更新 / 重名冲突 / 跳过 / 失败。 */
  status: "created" | "updated" | "conflict" | "skipped" | "failed";
  /** 失败或跳过时的原因说明。 */
  reason?: string;
  /** 导入成功（created/updated）时的技能本体。 */
  skill?: Skill;
  /** 发生重名冲突时的既有技能信息。 */
  existing_skill?: {
    id: string;
    name: string;
    created_by?: string;
    /** 当前用户是否允许覆盖它。 */
    can_overwrite?: boolean;
  };
}

/** 更新技能的请求体：字段全部可选，传哪个改哪个。 */
export interface UpdateSkillRequest {
  name?: string;
  description?: string;
  content?: string;
  config?: Record<string, unknown>;
  files?: { id?: string; path: string; content: string; kind?: "text" | "binary"; mode?: number }[];
}

/** 请求体：一次性设置某 agent 挂载哪几个工作区技能（整表替换）。 */
export interface SetAgentSkillsRequest {
  skill_ids: string[];
}

/**
 * 一个 issue 累计的 token 用量汇总（getIssueUsage 返回）。
 * 成本分列字段是可选的（见下方 RuntimeUsage 注释对 `cost_usd_ticks` +
 * `uncosted_*` 的说明）。
 */
export interface IssueUsageSummary {
  total_input_tokens: number;
  total_output_tokens: number;
  total_cache_read_tokens: number;
  total_cache_write_tokens: number;
  // 这里与用量行类型不同，它是可选的：`getIssueUsage` 原样返回该结构
  // （无 zod schema 校验），当后端早于成本拆分上线时没有任何东西保证
  // 该字段一定存在。
  cost_usd_ticks?: number;
  uncosted_input_tokens?: number;
  uncosted_output_tokens?: number;
  uncosted_cache_read_tokens?: number;
  uncosted_cache_write_tokens?: number;
  // Coverage fields are optional for compatibility with older backends.
  // task_count remains the legacy count of runs represented by usage rows.
  terminal_task_count?: number;
  metered_task_count?: number;
  unreported_task_count?: number;
  task_count: number;
}

// `cost_usd_ticks` + `uncosted_*`：每条用量行都携带的成本拆分。五个全是
// 可选的：拆分上线前的旧后端一个都不发，而且 `undefined` 必须能跟真正的 0
// 区分开（见下）。`cost_usd_ticks` 是 provider 对这批行自报的成本（1e-10
// 美元）；`uncosted_*` 是它没有报价的 token——只有这部分才该拿客户端的
// 价目表去估。`uncosted_*` 可选是因为旧后端缺省它们——这里的 `undefined`
// 表示"从完整 token 数估算"，和真正的 0（"没剩什么可估"）不是一回事。
// 参见 packages/views/runtimes/utils.ts 的 estimateCost。
export interface RuntimeUsage {
  /** 归属的运行时 id。 */
  runtime_id: string;
  /** 统计日期（某一天）。 */
  date: string;
  provider: string;
  model: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  cost_usd_ticks?: number;
  uncosted_input_tokens?: number;
  uncosted_output_tokens?: number;
  uncosted_cache_read_tokens?: number;
  uncosted_cache_write_tokens?: number;
}

/** 运行时一天之内按小时聚合的活动量（如每小时跑了几次任务）。 */
export interface RuntimeHourlyActivity {
  /** 小时序号 0..23。 */
  hour: number;
  /** 该小时内的发生次数。 */
  count: number;
}

/**
 * 运行时详情页 "Cost by agent"（按 agent 成本）页签里的一个
 * (agent, provider, model) 行。provider + model 留在线上是因为成本在客户端
 * 按"每模型价目表"算（provider 用来区分不同厂商间撞名的裸模型 id）——客户端
 * 把这些行按 agent_id 分组、再把各模型的成本加总到每个 agent 上。
 */
export interface RuntimeUsageByAgent {
  agent_id: string;
  provider: string;
  model: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  cost_usd_ticks?: number;
  uncosted_input_tokens?: number;
  uncosted_output_tokens?: number;
  uncosted_cache_read_tokens?: number;
  uncosted_cache_write_tokens?: number;
  /** 该 (agent, provider, model) 的任务数。 */
  task_count: number;
}

/**
 * "By hour"（按小时）页签里的一个 (hour, model) 行；hour ∈ 0..23。
 * 零活动的小时服务器不返回；客户端自行补空以渲染连续坐标轴。model 保留
 * 是为了客户端算成本。
 */
export interface RuntimeUsageByHour {
  hour: number;
  model: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  cost_usd_ticks?: number;
  uncosted_input_tokens?: number;
  uncosted_output_tokens?: number;
  uncosted_cache_read_tokens?: number;
  uncosted_cache_write_tokens?: number;
  task_count: number;
}

/**
 * 工作区看板用的 (date, provider, model) token 用量桶。工作区范围（没有
 * runtime_id），可被服务器侧收窄到单个项目。`provider` 留在线上让客户端在
 * 计价时区分跨厂商撞名的裸模型 id（比如 Cursor 的 `auto` 和别的厂商的
 * `auto`）。成本仍由客户端的模型价目表计算。
 */
export interface DashboardUsageDaily {
  date: string;
  provider: string;
  model: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  cost_usd_ticks?: number;
  uncosted_input_tokens?: number;
  uncosted_output_tokens?: number;
  uncosted_cache_read_tokens?: number;
  uncosted_cache_write_tokens?: number;
  task_count: number;
}

/**
 * 工作区看板的按 (agent, model) token 总量。线上结构与 RuntimeUsageByAgent
 * 完全相同——客户端按 agent_id 折叠并加总成本。
 */
export interface DashboardUsageByAgent {
  agent_id: string;
  provider: string;
  model: string;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_write_tokens: number;
  cost_usd_ticks?: number;
  uncosted_input_tokens?: number;
  uncosted_output_tokens?: number;
  uncosted_cache_read_tokens?: number;
  uncosted_cache_write_tokens?: number;
  task_count: number;
}

/**
 * 每个 agent 的终态任务总运行时长 + 数量。支撑工作区看板 "time by agent"
 * （按 agent 用时）列表。failed_count 是 task_count 的子集（失败任务照样
 * 贡献 total_seconds，因为它们消耗了运行时才失败的）。
 */
export interface DashboardAgentRunTime {
  agent_id: string;
  /** 总运行秒数。 */
  total_seconds: number;
  /** 任务总数。 */
  task_count: number;
  // Optional for compatibility with backends predating usage-coverage
  // reporting. Consumers can still identify the fully-unreported case when
  // this is absent by checking whether the agent has any usage rows.
  metered_task_count?: number;
  failed_count: number;
  // 用户中途停掉的任务数。与 `failed_count` 互斥，二者都是 `task_count` 的
  // 子集——成功数就是余下的那部分。被停的运行也占了 agent、也花了 token，
  // 所以它的秒数同样计入 `total_seconds`。
  cancelled_count: number;
}

/**
 * 工作区看板的按 (date) 终态任务运行时长 + 数量桶。支撑每日趋势切换的
 * Time 和 Tasks 指标——和 Tokens/Cost 同一个切换器，都锚定 completed_at，
 * 让日桶与"按 agent 运行时长"卡片对得上。
 */
export interface DashboardRunTimeDaily {
  date: string;
  total_seconds: number;
  task_count: number;
  failed_count: number;
  // 含义见 DashboardAgentRunTime.cancelled_count。
  cancelled_count: number;
}

/**
 * 工作区看板 Errors（错误）指标的 (date, failure_reason) 终态任务计数桶。
 *
 * `failure_reason` 携带后端权威的失败分类（taskfailure.Reason 的 21 个值，
 * 加上给"失败但列为空"用的 `"unclassified"`）——空串除外：那是*成功*桶。
 * 故意把成功行也放进同一条序列：这样错误率的分母和分子建立在完全相同的
 * 筛选条件下。`DashboardRunTimeDaily.task_count` 在这里不是安全的分母，因为
 * 它只统计真正开工的任务，而排队过期的任务从未开工。
 */
export interface DashboardFailureDaily {
  date: string;
  failure_reason: string;
  task_count: number;
}

/**
 * 按 (agent, failure_reason) 的终态任务计数。与 DashboardFailureDaily 同样的
 * 成功桶约定，客户端能按失败率给 agent 排名，而不是按原始失败数。
 */
export interface DashboardFailureByAgent {
  agent_id: string;
  failure_reason: string;
  task_count: number;
}

/** 运行时升级任务的进度状态：等待 / 升级中 / 完成 / 失败 / 超时。 */
export type RuntimeUpdateStatus =
  | "pending"
  | "running"
  | "completed"
  | "failed"
  | "timeout";

/** 一次"给运行时升级版本"的任务记录（daemon 侧执行）。 */
export interface RuntimeUpdate {
  id: string;
  /** 被升级的运行时 id。 */
  runtime_id: string;
  /** 当前进度状态。 */
  status: RuntimeUpdateStatus;
  /** 升级目标版本号。 */
  target_version: string;
  /** 升级过程输出（可选）。 */
  output?: string;
  /** 失败原因（可选）。 */
  error?: string;
  created_at: string;
  updated_at: string;
}

/** 运行时对外提供的一个可用模型及其附加能力（daemon 探测上报）。 */
export interface RuntimeModel {
  id: string;
  /** 展示名（人读的标签）。 */
  label: string;
  /** 所属 provider（可选）。 */
  provider?: string;
  /** 是否为该运行时的默认模型。 */
  default?: boolean;
  /**
   * daemon 探测到的"该模型支持哪些推理强度档"。目前 claude、codex、opencode
   * 运行时会给；其他 provider 缺省（或 undefined），UI 视作"该模型没有
   * 推理强度选择器"。见 MUL-2339。
   */
  thinking?: RuntimeModelThinking;
  /** 该模型对外宣告的运行时原生执行档位。 */
  service_tiers?: RuntimeModelServiceTier[];
  /**
   * 该运行时装的 Codex CLI 是否接受 request-only 的 `default` 哨兵值（显式
   * 标准路由）。缺失按 false 处理——老 daemon 面前新客户端保持安全。
   */
  supports_explicit_standard_service_tier?: boolean;
}

/** 一个运行时原生执行档位（服务档位，如 Codex 的 Fast/Priority）。 */
export interface RuntimeModelServiceTier {
  /** 原样发给 provider 协议的目录 id。 */
  id: string;
  /** provider 给的展示名，如 `Fast`。 */
  name: string;
  /** provider 给的辅助文案（可选）。 */
  description?: string;
}

/** 某模型对"推理强度档"的能力描述（哪些档可挑、默认档是什么）。 */
export interface RuntimeModelThinking {
  /** 该模型允许用户挑选的档位。 */
  supported_levels: RuntimeModelThinkingLevel[];
  /** 参考信息：上游 CLI 文档声明的不传 --effort 时的内置默认档。daemon 会
   *  上报，但今天没有真正渲染它——Multica 的空 `thinking_level` 意思是
   *  "不覆盖，让本地 CLI 配置决定"，那本身可能就与这个值不同。 */
  default_level?: string;
}

/** 单个推理强度档位。 */
export interface RuntimeModelThinkingLevel {
  /** 原样传给 CLI 的运行时原生 token，从不归一化。 */
  value: string;
  /** 与各 CLI 自家 UI 一致的展示名（`Low`、`Extra high`…）。 */
  label: string;
  /** 可选辅助文案，取自上游目录（`codex debug models` 每个档发一条）。 */
  description?: string;
}

/** 拉取模型清单任务的进度状态。 */
export type RuntimeModelListStatus =
  | "pending"
  | "running"
  | "completed"
  | "failed"
  | "timeout";

/** 一次"拉取运行时可用模型清单"任务的结果记录。 */
export interface RuntimeModelListRequest {
  id: string;
  runtime_id: string;
  status: RuntimeModelListStatus;
  /** 探测出的模型列表（完成时有）。 */
  models?: RuntimeModel[];
  /** 该运行时是否支持"按 agent 选模型"。 */
  supported: boolean;
  /** 失败原因（可选）。 */
  error?: string;
  created_at: string;
  updated_at: string;
  /**
   * 服务器是否直接用自己缓存的目录回答、而没有真跑一趟 daemon 往返
   * （MUL-5444）。仅参考：这种响应到达时已带 `status: "completed"` 和填充好
   * 的 `models`，忽略该字段的调用方行为与之前完全一致。`cached_at` 是这份
   * 快照的抓取时间。
   */
  cached?: boolean;
  cached_at?: string;
}

/**
 * resolveRuntimeModels 返回的结果形态——带 "supported" 位，让 UI 区分
 * "没探测到模型" 和 "provider 不支持按 agent 选模型"。
 */
export interface RuntimeModelsResult {
  models: RuntimeModel[];
  supported: boolean;
  /**
   * 服务器是否用目录缓存而非真跑 daemon 往返作答（MUL-5444）。驱动查询的
   * 新鲜度策略：缓存答案可以立刻重校验，客户端绝不会延长服务器的过期窗口。
   */
  cached?: boolean;
  /** 响应来自缓存时，所供快照的抓取时间。 */
  cachedAt?: string;
}

/** 拉取"运行时本地技能"清单的任务状态（conflict=本地与工作区同名冲突）。 */
export type RuntimeLocalSkillStatus =
  | "pending"
  | "running"
  | "completed"
  | "conflict"
  | "failed"
  | "timeout";

/** 处理同名冲突时可选的覆盖动作：目前只有 overwrite（覆盖）。 */
export type RuntimeLocalSkillImportAction = "overwrite";

/** 把本地技能导入工作区时遇到的同名冲突信息。 */
export interface RuntimeLocalSkillImportConflict {
  /** 撞名的工作区既有技能 id。 */
  existing_skill_id: string;
  /** 既有技能创建者（可选）。 */
  existing_created_by?: string;
  /** 当前用户是否有权覆盖。 */
  can_overwrite: boolean;
}

/** 运行时本地扫描到的一条技能摘要。 */
export interface RuntimeLocalSkillSummary {
  /** 技能稳定键。 */
  key: string;
  /** 技能名。 */
  name: string;
  /** 一句话描述（可选）。 */
  description?: string;
  /** 技能在磁盘上的来源路径。 */
  source_path: string;
  /** 所属 provider。 */
  provider: string;
  /**
   * 这条技能从哪个发现根冒出："provider" 是运行时自带的技能目录（如
   * ~/.claude/skills），"universal" 是跨工具的 ~/.agents/skills 兜底目录。
   * 早于多根发现的老 daemon 缺省该字段——`undefined` 按"未知"处理，别断言
   * 是哪个来源。
   */
  root?: "provider" | "universal" | "plugin";
  /** 贡献这条技能的已启用运行时插件（如有）。 */
  plugin?: string;
  /** 新 daemon 只在能执行"按 agent 禁用"时才置 true。 */
  can_disable?: boolean;
  /** 技能含有的文件数量。 */
  file_count: number;
}

/** 运行时本地探测到的一个 MCP 服务器摘要。 */
export interface RuntimeLocalMcpServerSummary {
	/** MCP 服务器名。 */
	name: string;
	/** 传输方式：标准输入输出 / http / sse / 未知。 */
	transport?: "stdio" | "http" | "sse" | "unknown";
	/** 配置来源（可选）。 */
	source?: string;
	/** 是否启用。 */
	enabled: boolean;
}

/** 一次"拉取运行时本地技能清单"的任务结果记录。 */
export interface RuntimeLocalSkillListRequest {
  id: string;
  runtime_id: string;
  status: RuntimeLocalSkillStatus;
  /** 扫描到的技能列表（完成时有）。 */
  skills?: RuntimeLocalSkillSummary[];
  supported: boolean;
	mcp_servers?: RuntimeLocalMcpServerSummary[];
	mcp_supported?: boolean;
  /** 失败原因（可选）。 */
  error?: string;
  created_at: string;
  updated_at: string;
}

/** 请求体：把一条运行时本地技能导入工作区（含冲突时是否覆盖）。 */
export interface CreateRuntimeLocalSkillImportRequest {
  /** 要导入的本地技能键。 */
  skill_key: string;
  name?: string;
  description?: string;
  /** 冲突时是否覆盖（见 RuntimeLocalSkillImportAction）。 */
  action?: RuntimeLocalSkillImportAction;
  /** 覆盖模式下目标工作区技能 id。 */
  target_skill_id?: string;
  /** 该技能是否支持冲突上报。 */
  supports_conflict?: boolean;
}

/** 一次"导入本地技能"任务的结果记录。 */
export interface RuntimeLocalSkillImportRequest {
  id: string;
  runtime_id: string;
  skill_key: string;
  name?: string;
  description?: string;
  action?: RuntimeLocalSkillImportAction;
  target_skill_id?: string;
  supports_conflict?: boolean;
  status: RuntimeLocalSkillStatus;
  /** 导入成功的技能本体。 */
  skill?: Skill;
  /** 撞名时的冲突信息。 */
  conflict?: RuntimeLocalSkillImportConflict;
  /** 失败原因。 */
  error?: string;
  created_at: string;
  updated_at: string;
}

/** 拉取"运行时本地技能清单"的返回结果（客户端最终拿到的形态）。 */
export interface RuntimeLocalSkillsResult {
  skills: RuntimeLocalSkillSummary[];
  /** 该运行时是否支持按 agent 挂技能。 */
  supported: boolean;
	mcpServers: RuntimeLocalMcpServerSummary[];
	mcpSupported: boolean;
}

/** 同步接口形态的本地技能导入结果（无需轮询任务）。 */
export interface RuntimeLocalSkillImportResult {
  status: "created" | "updated" | "conflict";
  skill?: Skill;
  conflict?: RuntimeLocalSkillImportConflict;
}
