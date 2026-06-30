export const TRAINING_ROUTES = [
  { query: "提示词库", command: "打开提示词库", path: "prompts", nav: "提示词库", text: "提示词库", showPromptEditor: true, showPromptPlayground: false, showAgentWorkbench: false },
  { query: "调试运行", command: "打开调试运行", path: "debug-runs", nav: "调试运行", text: "调试运行", showPromptEditor: false, showPromptPlayground: true, showAgentWorkbench: false },
  { query: "数据集", command: "打开数据集", path: "datasets", nav: "数据集", text: "数据集", showPromptEditor: false, showPromptPlayground: false, showAgentWorkbench: false },
  { query: "测试套件", command: "打开测试套件", path: "test-suites", nav: "测试套件", text: "测试套件", showPromptEditor: false, showPromptPlayground: false, showAgentWorkbench: false },
] as const;

export const DEFAULT_TRAINING_ROUTE = TRAINING_ROUTES[0];

export function trainingRoutePath(workspaceSlug: string, path: string) {
  return `/${workspaceSlug}/training/${path}`;
}
