export const TRAINING_WORKBENCH_VIEWS = [
  {
    tab: "运行看板",
    view: "runs",
    route: "runs",
    keywords: ["运行看板", "验收", "运行", "demo", "dashboard", "observability"],
  },
  {
    tab: "提示词库",
    view: "prompts",
    route: "prompts",
    keywords: ["提示词库", "提示词管理", "prompt", "library", "prompts"],
  },
  {
    tab: "提示词调试场",
    view: "prompt-playground",
    route: "prompt-playground",
    keywords: ["提示词调试", "prompt", "playground", "debug"],
  },
  {
    tab: "Agent 调试场",
    view: "agent-playground",
    route: "agent-playground",
    keywords: ["Agent 调试", "agent", "playground", "debug"],
  },
  {
    tab: "数据集",
    view: "datasets",
    route: "datasets",
    keywords: ["数据集", "dataset", "training", "data"],
  },
  {
    tab: "测试套件",
    view: "test-suites",
    route: "test-suites",
    keywords: ["测试套件", "test", "suite", "eval"],
  },
  {
    tab: "实验",
    view: "experiments",
    route: "experiments",
    keywords: ["实验", "experiment", "对比"],
  },
  {
    tab: "优化运行",
    view: "optimization-runs",
    route: "optimization-runs",
    keywords: ["优化运行", "optimization"],
  },
  {
    tab: "运行历史",
    view: "run-history",
    route: "run-history",
    keywords: ["运行历史", "history", "trace"],
  },
] as const;

export type TrainingWorkbenchView = typeof TRAINING_WORKBENCH_VIEWS[number];
export type TrainingWorkbenchTab = TrainingWorkbenchView["tab"];
export type TrainingWorkbenchViewId = TrainingWorkbenchView["view"];
export type TrainingWorkbenchRoute = TrainingWorkbenchView["route"];

export const TRAINING_WORKBENCH_TABS = TRAINING_WORKBENCH_VIEWS.map((item) => item.tab) as TrainingWorkbenchTab[];
export const DEFAULT_TRAINING_WORKBENCH_TAB: TrainingWorkbenchTab = "运行看板";
export const DEFAULT_TRAINING_WORKBENCH_VIEW: TrainingWorkbenchViewId = "runs";
export const DEFAULT_TRAINING_WORKBENCH_ROUTE: TrainingWorkbenchRoute = "runs";

export const TRAINING_WORKBENCH_VIEW_BY_TAB = Object.fromEntries(
  TRAINING_WORKBENCH_VIEWS.map((item) => [item.tab, item.view]),
) as Record<TrainingWorkbenchTab, TrainingWorkbenchViewId>;

export const TRAINING_WORKBENCH_TAB_BY_VIEW = Object.fromEntries(
  TRAINING_WORKBENCH_VIEWS.map((item) => [item.view, item.tab]),
) as Record<TrainingWorkbenchViewId, TrainingWorkbenchTab>;

export const TRAINING_WORKBENCH_ROUTE_BY_VIEW = Object.fromEntries(
  TRAINING_WORKBENCH_VIEWS.map((item) => [item.view, item.route]),
) as Record<TrainingWorkbenchViewId, TrainingWorkbenchRoute>;

export const TRAINING_WORKBENCH_VIEW_BY_ROUTE = Object.fromEntries(
  TRAINING_WORKBENCH_VIEWS.map((item) => [item.route, item.view]),
) as Record<TrainingWorkbenchRoute, TrainingWorkbenchViewId>;

const LEGACY_TRAINING_WORKBENCH_VIEW_ALIASES: Record<string, TrainingWorkbenchViewId> = {
  "demo-dashboard": "runs",
};

function normalizeTrainingWorkbenchView(view: string | null): TrainingWorkbenchViewId {
  if (!view) return DEFAULT_TRAINING_WORKBENCH_VIEW;
  const aliased = LEGACY_TRAINING_WORKBENCH_VIEW_ALIASES[view] ?? view;
  return TRAINING_WORKBENCH_TAB_BY_VIEW[aliased as TrainingWorkbenchViewId]
    ? (aliased as TrainingWorkbenchViewId)
    : DEFAULT_TRAINING_WORKBENCH_VIEW;
}

export function trainingWorkbenchViewFromRoute(route: string | null): TrainingWorkbenchViewId {
  if (!route) return DEFAULT_TRAINING_WORKBENCH_VIEW;
  return TRAINING_WORKBENCH_VIEW_BY_ROUTE[route as TrainingWorkbenchRoute] ?? normalizeTrainingWorkbenchView(route);
}

export function trainingWorkbenchTabFromView(view: string | null): TrainingWorkbenchTab {
  return TRAINING_WORKBENCH_TAB_BY_VIEW[normalizeTrainingWorkbenchView(view)];
}

export function trainingWorkbenchTitleFromView(view: string | null): string {
  return `训练与评估 · ${trainingWorkbenchTabFromView(view)}`;
}

export function trainingWorkbenchPath(baseTrainingPath: string, view: string | null): string {
  const route = TRAINING_WORKBENCH_ROUTE_BY_VIEW[normalizeTrainingWorkbenchView(view)];
  return `${baseTrainingPath.replace(/\/$/, "")}/${route}`;
}
