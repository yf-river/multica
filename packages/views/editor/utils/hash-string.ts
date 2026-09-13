/**
 * DJB2 字符串哈希——32 位无符号整数，最后以 base36 渲染。
 *
 * 从 `rich-content/mounted-block-registry.ts` 抽出来的（Mermaid 布局缓存
 * 用的就是这种廉价同步哈希）。HTML 附件预览用它对渲染后的文本推导
 * `contentKey` 指纹，这样滚动恢复能识别出附件内容是否已变化（同一 id
 * 被重新上传），从而拒绝恢复过期的滚动位置。
 */
export function hashString(source: string): string {
  let hash = 5381;
  for (let i = 0; i < source.length; i++) {
    hash = ((hash << 5) + hash) ^ source.charCodeAt(i);
  }
  return (hash >>> 0).toString(36);
}
