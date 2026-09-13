/**
 * navigation（导航）模块的公共出口。
 * 集中导出跨平台导航适配层：NavigationProvider/useNavigation、应用内链接
 * AppLink、点击意图解析、行链接 hook 与返回/前向导航等。
 */
export {
  NavigationProvider,
  useNavigation,
  useOptionalNavigation,
  useIsNavigating,
  useReportNavigating,
} from "./context";
export { AppLink } from "./app-link";
export { currentPath } from "./current-path";
export { resolveClickIntent } from "./click-intent";
export type { LinkClickIntent } from "./click-intent";
export { useAppOrigin } from "./use-app-origin";
export { useRowLink, rowLinkInteractiveProps } from "./use-row-link";
export { useIntentNavigate } from "./use-intent-navigate";
export { useBackOrReplace } from "./use-back-or-replace";
export type { NavigationAdapter } from "./types";
