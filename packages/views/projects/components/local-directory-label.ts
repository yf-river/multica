// 本地目录资源的显示名解析：兼容不同来源的行命名，保证一行资源始终有名字可显示。

import type {
  LocalDirectoryResourceRef,
  ProjectResource,
} from "@multica/core/types";

/**
 * 取一条 local_directory 行的显示名。
 *
 * 一行的名字可能存两处：顶层 `label` 列（改名后不再重发 ref 的版本开始写这里，
 * 见 project-resources-section.tsx 的 handleRenameLocalDirectory），以及 ref 里的
 * 历史副本（桌面版一直到 v0.4.28 都只知道这个位置）。现在的服务端每次写入都会
 * 让两处保持一致，但更老的服务端写的行仍可能不一致；而老客户端创建的行只有
 * ref 里的副本。列名比 ref 副本优先，因为当前客户端的改名写的就是它；路径名
 * 是最后的兜底，保证一行永远不会没有名字可显示。
 *
 * @param resource 带 label 与本地目录 resource_ref 的资源行
 * @returns 优先 label → ref.label → ref.local_path 的显示名
 */
export function localDirectoryLabel(
  resource: Pick<ProjectResource, "label"> & {
    resource_ref: LocalDirectoryResourceRef;
  },
): string {
  const ref = resource.resource_ref;
  return (
    (resource.label || ref.label || ref.local_path).trim() ||
    ref.local_path.trim()
  );
}
