/**
 * 富文本模块的公共出口。
 * 集中导出只读内容渲染器 RichContent、富围栏判断与流式围栏闭合计算。
 */
export {
  RichContent,
  type RichContentProps,
  type RichContentDensity,
  type RichContentPhase,
} from "./rich-content";
export {
  isRichFenceLanguage,
  shouldUpgradeFence,
  type RichFenceLanguage,
} from "./rich-code-block";
export { computeClosedFenceOffsets } from "./streaming-fence";
