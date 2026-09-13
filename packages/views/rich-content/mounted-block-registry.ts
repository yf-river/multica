/**
 * 富文本模块：记录本页面会话里哪些富块已经挂载过。
 *
 * 「只挂载一次」的保证不能只靠组件状态。Chat 列表是虚拟化的：滚得足够远
 * 时整行被卸载，本地 `mounted` 标志也随之消失。滚回来会重跑 Mermaid、
 * 重建一个全新的沙箱 iframe、丢弃查看者的平移/缩放——懒加载外壳本要避免
 * 的重复成本，每一趟都会重新出现。
 *
 * 以「语言 + 源码」的哈希为 key，这样同一张图无论在哪里重现（被回收的
 * Virtuoso 行、live -> persisted 的交接、或同一内容被引用两处）都能被认出。
 *
 * 刻意做成模块级而不是 store：这是渲染记账，不是应用状态。它从不持久化，
 * 也永不在服务端读取。
 */

// 设上限，避免超长会话无限增长。条目是短哈希串，淘汰策略最老优先（Set
// 保持插入顺序）；淘汰一个块只是让它再付一次挂载成本而已。
const MAX_TRACKED_BLOCKS = 500;

const mountedBlocks = new Set<string>();

// DJB2——与 Mermaid 布局缓存相同的廉价同步哈希。源码文本本身不适合做 key：
// 满是大图的记录会让每张图都活在这个 Set 里。
function hashSource(source: string): string {
  let hash = 5381;
  for (let i = 0; i < source.length; i++) {
    hash = ((hash << 5) + hash) ^ source.charCodeAt(i);
  }
  return (hash >>> 0).toString(36);
}

/** 生成某个富块的挂载登记 key。 */
export function mountedBlockKey(language: string, source: string): string {
  return `${language}:${hashSource(source)}`;
}

/** 判断某个块是否已经挂载过。 */
export function hasBlockMounted(key: string): boolean {
  return mountedBlocks.has(key);
}

/** 记录某个块已挂载，超出上限时淘汰最老的条目。 */
export function markBlockMounted(key: string): void {
  if (mountedBlocks.has(key)) return;
  if (mountedBlocks.size >= MAX_TRACKED_BLOCKS) {
    const oldest = mountedBlocks.values().next();
    if (!oldest.done) mountedBlocks.delete(oldest.value);
  }
  mountedBlocks.add(key);
}

/** 仅供测试：注册表按设计比单次渲染活得久。 */
export function resetMountedBlocks(): void {
  mountedBlocks.clear();
}
