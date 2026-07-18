export const TRAINING_WORKBENCH_VIEWS = [
  {
    tab: "提示词库",
    view: "prompts",
    canonicalRoute: "prompts",
    section: "debug",
    keywords: ["提示词库", "提示词管理", "prompt", "library", "prompts"],
  },
  {
    tab: "Agent 调试场",
    view: "agent-playground",
    canonicalRoute: "agent-playground",
    section: "debug",
    keywords: ["Agent 调试场", "智能体调试", "agent", "playground", "experiment"],
  },
  {
    tab: "用例库",
    view: "datasets",
    canonicalRoute: "datasets",
    section: "evaluation",
    keywords: ["用例库", "数据集", "dataset", "case", "training", "data"],
  },
  {
    tab: "测试套件",
    view: "test-suites",
    canonicalRoute: "test-suites",
    section: "evaluation",
    keywords: ["测试套件", "test", "suite", "eval"],
  },
  {
    tab: "评测记录",
    view: "evaluation-runs",
    canonicalRoute: "runs",
    section: "evaluation",
    keywords: ["评测记录", "运行证据", "evaluation", "runs", "evidence", "trace"],
  },
] as const;

type TrainingWorkbenchView = typeof TRAINING_WORKBENCH_VIEWS[number];
export type TrainingWorkbenchTab = TrainingWorkbenchView["tab"];
export type TrainingWorkbenchViewId = TrainingWorkbenchView["view"];
type TrainingWorkbenchCanonicalRoute = TrainingWorkbenchView["canonicalRoute"];
type TrainingWorkbenchSection = TrainingWorkbenchView["section"];

export const DEFAULT_DEBUG_WORKBENCH_VIEW: TrainingWorkbenchViewId = "prompts";
export const DEFAULT_EVALUATION_WORKBENCH_VIEW: TrainingWorkbenchViewId = "datasets";

export const TRAINING_WORKBENCH_VIEW_BY_TAB = Object.fromEntries(
  TRAINING_WORKBENCH_VIEWS.map((item) => [item.tab, item.view]),
) as Record<TrainingWorkbenchTab, TrainingWorkbenchViewId>;

const TRAINING_WORKBENCH_TAB_BY_VIEW = Object.fromEntries(
  TRAINING_WORKBENCH_VIEWS.map((item) => [item.view, item.tab]),
) as Record<TrainingWorkbenchViewId, TrainingWorkbenchTab>;

const TRAINING_WORKBENCH_CANONICAL_ROUTE_BY_VIEW = Object.fromEntries(
  TRAINING_WORKBENCH_VIEWS.map((item) => [item.view, item.canonicalRoute]),
) as Record<TrainingWorkbenchViewId, TrainingWorkbenchCanonicalRoute>;

const TRAINING_WORKBENCH_VIEW_BY_ROUTE = Object.fromEntries(
  TRAINING_WORKBENCH_VIEWS.map((item) => [item.canonicalRoute, item.view]),
) as Record<string, TrainingWorkbenchViewId>;

const TRAINING_WORKBENCH_SECTION_BY_VIEW = Object.fromEntries(
  TRAINING_WORKBENCH_VIEWS.map((item) => [item.view, item.section]),
) as Record<TrainingWorkbenchViewId, TrainingWorkbenchSection>;

export const TRAINING_WORKBENCH_VIEWS_BY_SECTION = {
  debug: TRAINING_WORKBENCH_VIEWS.filter((item) => item.section === "debug"),
  evaluation: TRAINING_WORKBENCH_VIEWS.filter((item) => item.section === "evaluation"),
} satisfies Record<TrainingWorkbenchSection, readonly TrainingWorkbenchView[]>;

const TRAINING_WORKBENCH_SECTION_LABEL_BY_SECTION: Record<TrainingWorkbenchSection, string> = {
  debug: "调试",
  evaluation: "评估",
};

function normalizeTrainingWorkbenchView(view: string | null): TrainingWorkbenchViewId {
  if (!view) return DEFAULT_DEBUG_WORKBENCH_VIEW;
  return TRAINING_WORKBENCH_TAB_BY_VIEW[view as TrainingWorkbenchViewId]
    ? (view as TrainingWorkbenchViewId)
    : DEFAULT_DEBUG_WORKBENCH_VIEW;
}

export function trainingWorkbenchViewFromCanonicalRoute(
  section: TrainingWorkbenchSection,
  route: string | null,
): TrainingWorkbenchViewId {
  const fallback = section === "debug" ? DEFAULT_DEBUG_WORKBENCH_VIEW : DEFAULT_EVALUATION_WORKBENCH_VIEW;
  if (!route) return fallback;
  const view = TRAINING_WORKBENCH_VIEW_BY_ROUTE[route];
  if (!view) return fallback;
  return TRAINING_WORKBENCH_SECTION_BY_VIEW[view] === section ? view : fallback;
}

export function trainingWorkbenchTabFromView(view: string | null): TrainingWorkbenchTab {
  return TRAINING_WORKBENCH_TAB_BY_VIEW[normalizeTrainingWorkbenchView(view)];
}

export function trainingWorkbenchSectionLabelFromView(view: string | null): string {
  return TRAINING_WORKBENCH_SECTION_LABEL_BY_SECTION[
    TRAINING_WORKBENCH_SECTION_BY_VIEW[normalizeTrainingWorkbenchView(view)]
  ];
}

export function trainingWorkbenchTitleFromView(view: string | null): string {
  return `${trainingWorkbenchSectionLabelFromView(view)} · ${trainingWorkbenchTabFromView(view)}`;
}

export function trainingWorkbenchShowsPromptEditor(view: string | null): boolean {
  return trainingWorkbenchTabFromView(view) === "提示词库";
}

export function debugWorkbenchPath(baseDebugPath: string, view: string | null): string {
  const normalizedView = normalizeTrainingWorkbenchView(view);
  const debugView = TRAINING_WORKBENCH_SECTION_BY_VIEW[normalizedView] === "debug"
    ? normalizedView
    : DEFAULT_DEBUG_WORKBENCH_VIEW;
  return `${baseDebugPath.replace(/\/$/, "")}/${TRAINING_WORKBENCH_CANONICAL_ROUTE_BY_VIEW[debugView]}`;
}

export function evaluationWorkbenchPath(baseEvaluationPath: string, view: string | null): string {
  const normalizedView = normalizeTrainingWorkbenchView(view);
  const evaluationView = TRAINING_WORKBENCH_SECTION_BY_VIEW[normalizedView] === "evaluation"
    ? normalizedView
    : DEFAULT_EVALUATION_WORKBENCH_VIEW;
  return `${baseEvaluationPath.replace(/\/$/, "")}/${TRAINING_WORKBENCH_CANONICAL_ROUTE_BY_VIEW[evaluationView]}`;
}

export function trainingWorkbenchCanonicalPath(
  paths: { debug: () => string; evaluation: () => string },
  view: string | null,
): string {
  const normalizedView = normalizeTrainingWorkbenchView(view);
  return TRAINING_WORKBENCH_SECTION_BY_VIEW[normalizedView] === "debug"
    ? debugWorkbenchPath(paths.debug(), normalizedView)
    : evaluationWorkbenchPath(paths.evaluation(), normalizedView);
}

export * from "./skill-scenarios";
