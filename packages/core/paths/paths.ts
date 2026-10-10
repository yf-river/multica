// 本文件是集中的 URL 路径构造器。共享包（packages/views）里所有导航 MUST
// 都走本模块——不允许硬编码字符串路径。
//
// 两种路径：
//  - 工作区范围：paths.workspace(slug).xxx() —— 把工作区带在 URL 里；
//  - 全局：paths.login()、paths.newWorkspace()、paths.invite(id) —— 工作区之前
//    的路由。
//
// 为什么用纯函数 + 构造器模式：
//  - 改路由形态（例如加工作区 slug 前缀）变成单文件修改；
//  - id 一律在这里做 URL 编码，调用方不会忘；
//  - 零运行时依赖，让本模块在 Node（测试）与浏览器里都安全。

const encode = (id: string) => encodeURIComponent(id);

/** life 页的标签清单。 */
export const LIFE_TABS = ["memory", "experiment", "observers", "chronicle"] as const;
export type LifeTab = (typeof LIFE_TABS)[number];

/**
 * `?focus=` 令牌：把 agent 的 Instructions 标签页滚到它的 conversation
 * starters 编辑器并闪烁提示。放在这里是因为它是 URL 词汇：`agentConversation
 * Starters()` 写它、标签页读它，共享常量才能防止两者漂移。
 */
export const AGENT_FOCUS_CONVERSATION_STARTERS = "conversation_starters";

/** 工作区范围内的路径构造器（slug 做 URL 编码）。 */
function workspaceScoped(slug: string) {
  const ws = `/${encode(slug)}`;
  return {
    root: () => `${ws}/issues`,
    usage: () => `${ws}/usage`,
    runReviews: () => `${ws}/run-reviews`,
    issues: () => `${ws}/issues`,
    issueDetail: (id: string) => `${ws}/issues/${encode(id)}`,
    projects: () => `${ws}/projects`,
    projectDetail: (id: string) => `${ws}/projects/${encode(id)}`,
    autopilots: () => `${ws}/autopilots`,
    autopilotDetail: (id: string) => `${ws}/autopilots/${encode(id)}`,
    agents: () => `${ws}/agents`,
    newAgent: () => `${ws}/agents/new`,
    // 选择器背后的两条创建方法。各自是真实路由，让填了一半的表单刷新后仍在、
    // 也可被直接链接到。
    newAgentManual: () => `${ws}/agents/new/manual`,
    newAgentAi: () => `${ws}/agents/new/ai`,
    // 一次创建会话。它是持久对象，不是上面路由的一步：离开工作室它仍在、
    // 之后可继续，所以它拥有自己的地址，而不是「开一个」屏幕上的一个查询参数。
    newAgentAiSession: (sessionId: string) =>
      `${ws}/agents/new/ai/${encode(sessionId)}`,
    agentDetail: (id: string) => `${ws}/agents/${encode(id)}`,
    // 聊天空态里「自定义」按钮背后的深链：agent 的 Instructions 标签页，
    // 滚到生成刚才看到的那些按钮的 conversation starters 处。
    agentConversationStarters: (id: string) =>
      `${ws}/agents/${encode(id)}?view=instructions&focus=${AGENT_FOCUS_CONVERSATION_STARTERS}`,
    memberDetail: (id: string) => `${ws}/members/${encode(id)}`,
    squads: () => `${ws}/squads`,
    squadDetail: (id: string) => `${ws}/squads/${encode(id)}`,
    inbox: () => `${ws}/inbox`,
    chat: () => `${ws}/chat`,
    chatWithAgent: (agentId: string) =>
      `${ws}/chat?agent=${encode(agentId)}`,
    chatSession: (sessionId: string) =>
      `${ws}/chat?session=${encode(sessionId)}`,
    myIssues: () => `${ws}/my-issues`,
    runtimes: () => `${ws}/runtimes`,
    runtimeDetail: (id: string) => `${ws}/runtimes/${encode(id)}`,
    runtimeSettings: (machineId: string, runtimeId: string) =>
      `${ws}/runtimes/${encode(machineId)}/runtime/${encode(runtimeId)}`,
    skills: () => `${ws}/skills`,
    health: () => `${ws}/health`,
    healthReport: (id: string) => `${ws}/health?report=${encode(id)}`,
    life: () => `${ws}/life`,
    companion: () => `${ws}/companion`,
    lifeTab: (tab: LifeTab) => `${ws}/life?tab=${encode(tab)}`,
    skillDetail: (id: string) => `${ws}/skills/${encode(id)}`,
    settings: () => `${ws}/settings`,
    attachmentPreview: (id: string) => `${ws}/attachments/${encode(id)}/preview`,
  };
}

export const paths = {
  workspace: workspaceScoped,

  // 全局（工作区之前）的路由
  login: () => "/login",
  newWorkspace: () => "/workspaces/new",
  invite: (id: string) => `/invite/${encode(id)}`,
  invitations: () => "/invitations",
  onboarding: () => "/onboarding",
  authCallback: () => "/auth/callback",
  root: () => "/",
};

export type WorkspacePaths = ReturnType<typeof workspaceScoped>;

// 匹配的是「前缀」而非 slug 名，因为要跟完整 URL 路径比。某路径是全局路径，
// 当它等于或始于其中任一前缀。
// 注意：`/workspaces/`（带尾斜杠）是前缀——`workspaces` 是保留名，任何以
// `/workspaces/...` 开头的路径都是系统拥有的，不是用户拥有的。
const GLOBAL_PREFIXES = ["/login", "/workspaces/", "/invite/", "/invitations", "/onboarding", "/auth/", "/logout", "/signup"];

/** 判断路径是否属于全局（工作区之外）路由。 */
export function isGlobalPath(path: string): boolean {
  return GLOBAL_PREFIXES.some((p) => path === p || path.startsWith(p));
}
