// Agent 的"派生状态"类型——在界面各处展示的用户可见状态（列表圆点、悬停卡、
// 状态行）。由前端从原始服务器数据（agent + runtime + 最近任务）计算得出；
// 后端从不认识这些枚举。
//
// 两条互相正交的维度，各自独立推导、只回答"此刻为真的是什么？"——历史/错误
// 上下文放在 Agent 详情页（Recent Work、failure_reason）和 Inbox 里，不放列表级
// 摘要状态：
//
//   1. AgentAvailability —— "这个 Agent 现在能接活吗？"
//      只依赖运行时（runtime）可达性。应用里所有圆点颜色都反映这一个维度；
//      绝不因为过去某次任务结果而粘在红色上。
//
//   2. Workload —— "这个 Agent 现在手头有什么？"
//      只依赖工作区的任务快照。三种状态，每种都指向明确的用户动作：
//        working → 有任务在跑，正常
//        queued  → 有排队任务但没在跑（若可用性为 offline/unstable 就=卡住；
//                  若 online 则只是派发间隙的瞬间态）
//        idle    → 没活可干
//      没有 failed / completed / cancelled——那些是历史，走 Recent Work + Inbox 呈现。

// 运行时可达性维度。`unstable` 是运行时清扫器宽限期（离线 < 5 分钟）内的过渡黄态；
// 没有新服务器数据时它会衰变成 `offline`——因此消费端 hook 要做 30 秒 presence 心跳。
//
// `archived` 是唯一的非运行时取值：它反映 Agent 的生命周期（agent.archived_at 已设），
// 而非其运行时。它优先级最高——在 deriveAgentPresenceDetail 里先于运行时健康推导——
// 这样一台已退役却还残留 online 运行时的 Agent 绝不会显示成"在线"。从这个联合类型
// 映射的每个圆点/标签（availabilityConfig）都免费渲染它；它刻意不在 availabilityOrder 里
// （已归档 Agent 有自己的列表视图，不是可用性过滤芯片）。
export type AgentAvailability =
  | "online" // 🟢 运行时在线且可达
  | "unstable" // 🟡 运行时 recently_lost（< 5 分钟）——过渡态
  | "offline" // ⚫ 运行时长期离线 / 缺失 / 从未注册
  | "archived"; // ⚫ 已设置 agent.archived_at——已退役，优先级高于运行时健康

// Agent 当前的任务负载。三种状态——永不含历史，永不做错误预判（那些归 Inbox +
// Recent Work）：
//
//   working → runningCount > 0。detail 对象上的 runningCount/queuedCount
//             保留细分供展示。
//   queued  → 没有在跑任务，但有 ≥1 排队/已派发。最常见意味着运行时离线、任务卡住
//             等待；online 运行时在派发到运行之间的短暂闪现是无害竞态。
//   idle    → 手头没活。
//
// 与 availability 搭配看全貌：`online + working` 正常；`offline + queued` 是我们
// 明确要呈现的"卡住"态；`offline + idle` 是"Agent 不可用、也无任务在等"——都诚实。
export type Workload =
  | "working" // ≥1 个任务正在运行
  | "queued" // 没在跑，但有 ≥1 排队/已派发
  | "idle"; // 手头没活

/** Agent 呈现层状态的完整推导结果：可用性 + 负载 + 任务计数 + 并发上限。 */
export interface AgentPresenceDetail {
  /** 可用性维度（决定圆点颜色）。 */
  availability: AgentAvailability;
  /** 负载维度（决定状态文案）。 */
  workload: Workload;
  /** 正在运行的任务数。 */
  runningCount: number;
  /** 排队中的任务数。 */
  queuedCount: number;
  // 镜像 agent.max_concurrent_tasks——拉进 detail 里，界面渲染 `running / capacity`
  // 比例时不必重取 Agent。
  capacity: number;
}
