export const TRAINING_ROUTES = [
  { query: "运行看板", command: "打开运行看板", path: "runs", nav: "运行看板", text: "训练运行看板", showPromptEditor: false, showPromptPlayground: false, showAgentWorkbench: false },
  { query: "提示词库", command: "打开提示词库", path: "prompts", nav: "提示词库", text: "提示词库", showPromptEditor: true, showPromptPlayground: false, showAgentWorkbench: false },
  { query: "提示词调试", command: "打开提示词调试场", path: "prompt-playground", nav: "提示词调试场", text: "提示词调试场", showPromptEditor: false, showPromptPlayground: true, showAgentWorkbench: false },
  { query: "智能体调试", command: "打开智能体调试场", path: "agent-playground", nav: "智能体调试场", text: "智能体调试场", showPromptEditor: false, showPromptPlayground: false, showAgentWorkbench: true },
  { query: "数据集", command: "打开数据集", path: "datasets", nav: "数据集", text: "数据集", showPromptEditor: false, showPromptPlayground: false, showAgentWorkbench: false },
  { query: "测试套件", command: "打开测试套件", path: "test-suites", nav: "测试套件", text: "测试套件", showPromptEditor: false, showPromptPlayground: false, showAgentWorkbench: false },
  { query: "实验", command: "打开实验", path: "experiments", nav: "实验", text: "实验", showPromptEditor: false, showPromptPlayground: false, showAgentWorkbench: false },
  { query: "优化运行", command: "打开优化运行", path: "optimization-runs", nav: "优化运行", text: "优化运行", showPromptEditor: false, showPromptPlayground: false, showAgentWorkbench: false },
  { query: "运行历史", command: "打开运行历史", path: "run-history", nav: "运行历史", text: "运行历史", showPromptEditor: false, showPromptPlayground: false, showAgentWorkbench: false },
] as const;

export const DEFAULT_TRAINING_ROUTE = TRAINING_ROUTES[0];

export function trainingRoutePath(workspaceSlug: string, path: string) {
  return `/${workspaceSlug}/training/${path}`;
}
