export const TRAINING_ROUTES = [
  { section: "调试", query: "提示词库", command: "打开提示词库", path: "prompts", nav: "提示词库", text: "提示词库", pageKind: "training", showPromptEditor: true, showCaseLibrary: false, introRoute: null, panelRoute: null, introTitle: null, operatingText: null },
  { section: "调试", query: "智能体调试", command: "打开智能体调试场", path: "agent-playground", nav: "Agent 调试场", text: "Agent 调试场", pageKind: "agent-playground", showPromptEditor: false, showCaseLibrary: false, introRoute: null, panelRoute: null, introTitle: null, operatingText: null },
  { section: "评估", query: "用例库", command: "打开用例库", path: "datasets", nav: "用例库", text: "用例库", pageKind: "training", showPromptEditor: false, showCaseLibrary: true, introRoute: null, panelRoute: null, introTitle: null, operatingText: null },
  { section: "评估", query: "测试套件", command: "打开测试套件", path: "test-suites", nav: "测试套件", text: "测试套件", pageKind: "training", showPromptEditor: false, showCaseLibrary: false, introRoute: "test-suites", panelRoute: "test-suites", introTitle: "测试套件回归", operatingText: "固定试卷、断言回归、失败定位" },
  { section: "评估", query: "评测记录", command: "打开评测记录", path: "evaluation-runs", nav: "评测记录", text: "评测记录", pageKind: "training", showPromptEditor: false, showCaseLibrary: false, introRoute: "runs", panelRoute: "run-history", introTitle: "评测记录与证据", operatingText: "运行检索、证据展开、人工复核" },
] as const;

export function trainingRoutePath(workspaceSlug: string, path: string) {
  return `/${workspaceSlug}/${trainingRouteURLPath(path)}`;
}

export function trainingRouteURLPath(path: string) {
  if (path === "prompts") return "debug/prompts";
  if (path === "agent-playground") return "debug/agent-playground";
  if (path === "evaluation-runs") return "evaluation/runs";
  return `evaluation/${path}`;
}
