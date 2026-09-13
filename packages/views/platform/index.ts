/**
 * platform（平台适配）模块的公共出口。
 * 集中导出桌面拖拽条、站外打开 URL、桌面未读角标与滚动恢复（scroll
 * restoration）相关组件/Hook。
 */
export { DragStrip } from "./drag-strip";
export { openExternal } from "./open-external";
export { useDesktopUnreadBadge } from "./use-desktop-unread-badge";
export {
  ScrollRestorationProvider,
  useScrollRestorationAdapter,
  useRestoredScrollEntry,
  useRestoredScrollOffset,
  useRestoredScrollRef,
  useRestoredViewState,
  useViewStateWriter,
  type ScrollRestorationAdapter,
  type ScrollRestorationEntry,
  type ExternalScrollSource,
} from "./scroll-restoration";
