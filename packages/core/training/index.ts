export const TRAINING_WORKBENCH_VIEWS = [
  {
    tab: "演示看板",
    view: "demo-dashboard",
    keywords: ["演示看板", "领导视角", "验收", "demo", "dashboard", "observability"],
  },
  {
    tab: "提示词库",
    view: "prompts",
    keywords: ["提示词库", "prompt", "library", "prompts"],
  },
  {
    tab: "提示词调试场",
    view: "prompt-playground",
    keywords: ["提示词调试", "prompt", "playground", "debug"],
  },
  {
    tab: "Agent 调试场",
    view: "agent-playground",
    keywords: ["Agent 调试", "agent", "playground", "debug"],
  },
  {
    tab: "数据集",
    view: "datasets",
    keywords: ["数据集", "dataset", "training", "data"],
  },
  {
    tab: "测试套件",
    view: "test-suites",
    keywords: ["测试套件", "test", "suite", "eval"],
  },
  {
    tab: "实验",
    view: "experiments",
    keywords: ["实验", "experiment", "对比"],
  },
  {
    tab: "优化运行",
    view: "optimization-runs",
    keywords: ["优化运行", "optimization"],
  },
  {
    tab: "运行历史",
    view: "run-history",
    keywords: ["运行历史", "history", "trace"],
  },
] as const;

export type TrainingWorkbenchView = typeof TRAINING_WORKBENCH_VIEWS[number];
export type TrainingWorkbenchTab = TrainingWorkbenchView["tab"];
export type TrainingWorkbenchViewId = TrainingWorkbenchView["view"];

export const TRAINING_WORKBENCH_TABS = TRAINING_WORKBENCH_VIEWS.map((item) => item.tab) as TrainingWorkbenchTab[];
export const DEFAULT_TRAINING_WORKBENCH_TAB: TrainingWorkbenchTab = "演示看板";
export const DEFAULT_TRAINING_WORKBENCH_VIEW: TrainingWorkbenchViewId = "demo-dashboard";

export const TRAINING_WORKBENCH_VIEW_BY_TAB = Object.fromEntries(
  TRAINING_WORKBENCH_VIEWS.map((item) => [item.tab, item.view]),
) as Record<TrainingWorkbenchTab, TrainingWorkbenchViewId>;

export const TRAINING_WORKBENCH_TAB_BY_VIEW = Object.fromEntries(
  TRAINING_WORKBENCH_VIEWS.map((item) => [item.view, item.tab]),
) as Record<TrainingWorkbenchViewId, TrainingWorkbenchTab>;

export function trainingWorkbenchTabFromView(view: string | null): TrainingWorkbenchTab {
  if (!view) return DEFAULT_TRAINING_WORKBENCH_TAB;
  return TRAINING_WORKBENCH_TAB_BY_VIEW[view as TrainingWorkbenchViewId] ?? DEFAULT_TRAINING_WORKBENCH_TAB;
}

export function trainingWorkbenchTitleFromView(view: string | null): string {
  return `训练与评估 · ${trainingWorkbenchTabFromView(view)}`;
}
