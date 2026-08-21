const TRAINING_WORKBENCH_ALL_VIEWS = [
  {
    tab: "提示词库",
    view: "prompts",
    canonicalRoute: "prompts",
    section: "debug",
    keywords: ["提示词库", "提示词管理", "prompt", "library", "prompts"],
    visible: true,
  },
  {
    tab: "Agent 调试场",
    view: "agent-playground",
    canonicalRoute: "agent-playground",
    section: "debug",
    keywords: ["Agent 调试场", "智能体调试", "agent", "playground", "experiment"],
    visible: true,
  },
  {
    tab: "用例库",
    view: "datasets",
    canonicalRoute: "datasets",
    section: "evaluation",
    keywords: ["用例库", "数据集", "dataset", "case", "training", "data"],
    visible: true,
  },
  {
    tab: "测试套件",
    view: "test-suites",
    canonicalRoute: "test-suites",
    section: "evaluation",
    keywords: ["测试套件", "test", "suite", "eval"],
    visible: true,
  },
  {
    tab: "评测记录",
    view: "evaluation-runs",
    canonicalRoute: "runs",
    section: "evaluation",
    keywords: ["评测记录", "运行证据", "evaluation", "runs", "evidence", "trace"],
    visible: true,
  },
] as const;

export const TRAINING_WORKBENCH_VIEWS = TRAINING_WORKBENCH_ALL_VIEWS.filter((item) => item.visible);
export const TRAINING_WORKBENCH_SECTIONS = [
  { key: "debug", label: "调试", defaultView: "prompts" },
  { key: "evaluation", label: "评估", defaultView: "datasets" },
] as const;

export type TrainingWorkbenchView = typeof TRAINING_WORKBENCH_ALL_VIEWS[number];
export type TrainingWorkbenchTab = TrainingWorkbenchView["tab"];
export type TrainingWorkbenchViewId = TrainingWorkbenchView["view"];
export type TrainingWorkbenchCanonicalRoute = TrainingWorkbenchView["canonicalRoute"];
export type TrainingWorkbenchSection = typeof TRAINING_WORKBENCH_SECTIONS[number]["key"];

export const TRAINING_WORKBENCH_TABS = TRAINING_WORKBENCH_VIEWS.map((item) => item.tab) as TrainingWorkbenchTab[];
export const DEFAULT_TRAINING_WORKBENCH_TAB: TrainingWorkbenchTab = "提示词库";
export const DEFAULT_TRAINING_WORKBENCH_VIEW: TrainingWorkbenchViewId = "prompts";
export const DEFAULT_DEBUG_WORKBENCH_VIEW: TrainingWorkbenchViewId = "prompts";
export const DEFAULT_EVALUATION_WORKBENCH_VIEW: TrainingWorkbenchViewId = "datasets";

export const TRAINING_WORKBENCH_VIEW_BY_TAB = Object.fromEntries(
  TRAINING_WORKBENCH_ALL_VIEWS.map((item) => [item.tab, item.view]),
) as Record<TrainingWorkbenchTab, TrainingWorkbenchViewId>;

export const TRAINING_WORKBENCH_TAB_BY_VIEW = Object.fromEntries(
  TRAINING_WORKBENCH_ALL_VIEWS.map((item) => [item.view, item.tab]),
) as Record<TrainingWorkbenchViewId, TrainingWorkbenchTab>;

export const TRAINING_WORKBENCH_CANONICAL_ROUTE_BY_VIEW = Object.fromEntries(
  TRAINING_WORKBENCH_ALL_VIEWS.map((item) => [item.view, item.canonicalRoute]),
) as Record<TrainingWorkbenchViewId, TrainingWorkbenchCanonicalRoute>;

export const TRAINING_WORKBENCH_VIEW_BY_ROUTE = Object.fromEntries(
  TRAINING_WORKBENCH_ALL_VIEWS.map((item) => [item.canonicalRoute, item.view]),
) as Record<string, TrainingWorkbenchViewId>;

export const TRAINING_WORKBENCH_SECTION_BY_VIEW = Object.fromEntries(
  TRAINING_WORKBENCH_ALL_VIEWS.map((item) => [item.view, item.section]),
) as Record<TrainingWorkbenchViewId, TrainingWorkbenchSection>;

export const TRAINING_WORKBENCH_VIEWS_BY_SECTION = Object.fromEntries(
  TRAINING_WORKBENCH_SECTIONS.map((section) => [
    section.key,
    TRAINING_WORKBENCH_VIEWS.filter((item) => item.section === section.key),
  ]),
) as Record<TrainingWorkbenchSection, typeof TRAINING_WORKBENCH_VIEWS>;

export const TRAINING_WORKBENCH_SECTION_LABEL_BY_SECTION = Object.fromEntries(
  TRAINING_WORKBENCH_SECTIONS.map((item) => [item.key, item.label]),
) as Record<TrainingWorkbenchSection, string>;

function normalizeTrainingWorkbenchView(view: string | null): TrainingWorkbenchViewId {
  if (!view) return DEFAULT_TRAINING_WORKBENCH_VIEW;
  return TRAINING_WORKBENCH_TAB_BY_VIEW[view as TrainingWorkbenchViewId]
    ? (view as TrainingWorkbenchViewId)
    : DEFAULT_TRAINING_WORKBENCH_VIEW;
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

export function trainingWorkbenchSectionFromView(view: string | null): TrainingWorkbenchSection {
  return TRAINING_WORKBENCH_SECTION_BY_VIEW[normalizeTrainingWorkbenchView(view)];
}

export function trainingWorkbenchSectionLabelFromView(view: string | null): string {
  return TRAINING_WORKBENCH_SECTION_LABEL_BY_SECTION[trainingWorkbenchSectionFromView(view)];
}

export function trainingWorkbenchTitleFromView(view: string | null): string {
  return `${trainingWorkbenchSectionLabelFromView(view)} · ${trainingWorkbenchTabFromView(view)}`;
}

export function trainingWorkbenchShowsPromptEditor(view: string | null): boolean {
  return trainingWorkbenchTabFromView(view) === "提示词库";
}

export function trainingWorkbenchCanonicalRouteFromView(view: string | null): string {
  return TRAINING_WORKBENCH_CANONICAL_ROUTE_BY_VIEW[normalizeTrainingWorkbenchView(view)];
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
