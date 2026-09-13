"use client";

/**
 * 内联媒体的重新签名（MUL-3254）。
 *
 * 从 editor/attachment.tsx 抽出，让预览弹窗也能做同样的升级：画廊导航
 * （MUL-5752）会直接把用户没点过的附件交给弹窗，弹窗不能再指望内联渲染器
 * 已经替它解析出了可加载的 URL。
 */

import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { api } from "@multica/core/api";
import { attachmentIdFromDownloadURL } from "@multica/core/types/attachment-url";

// 让重取保持在服务端签名 URL 的有效期（默认 30 分钟，
// server/internal/handler/file.go）之内，避免一次重渲染把已过期签名
// 从查询缓存里喂给用户。
const RESIGN_STALE_MS = 20 * 60 * 1000;

// 拉到的图片字节在最后一个使用它的 <img> 卸载后，还能在查询缓存里存活
// 多久。字节本身永不过期（一个附件 id 对应一个不可变存储对象），所以 blob
// 查询用 `staleTime: Infinity`；这个上限纯粹是为了避免在图片很多的长线程
// 里滚动时，把每一张解码后的截图永远钉在渲染进程内存中。
export const INLINE_BLOB_GC_MS = 5 * 60 * 1000;

// 按附件 id 缓存的 blob: URL（模块级）。没有它，useObjectURL 会在每次挂载
// 都新建一个对象 URL（state "" -> effect 创建 URL），导致已缓存的 blob
// 在重进时仍会闪烁一帧 fallback 的 pickedUrl —— 也就是 #7741 里图片很多
// 的工单所报告的那个闪烁。
const blobUrlCache = new Map<string, string>();
const blobUrlRefCount = new Map<string, number>();
const blobUrlGCTimers = new Map<string, ReturnType<typeof setTimeout>>();

/**
 * 仅供测试使用：清空模块级的 blob URL 缓存、引用计数与待执行 GC 定时器。
 */
export function __resetInlineMediaBlobCacheForTests(): void {
  for (const t of blobUrlGCTimers.values()) clearTimeout(t);
  blobUrlGCTimers.clear();
  blobUrlRefCount.clear();
  blobUrlCache.clear();
}

/**
 * useResignedInlineMediaURL 负责把受鉴权保护的媒体 URL 升级成一份新签名的
 * URL，供那些无法原生加载 `/api/attachments/<id>/download` 的客户端使用。
 *
 * 渲染器选中的内联 URL 有时会落到「按附件稳定的 API 地址」上（例如重开的
 * 工单草稿，落库记录会刻意剥掉短时效的签名 download_url）。该地址需要
 * 凭据才能访问：Web 端之所以能显示，是因为真正同源时会话 cookie 会随
 * <img> 请求一起带上。而 Desktop 的 file:// 渲染器、移动端 webview、以及
 * 跨源部署的 Web 都无法依赖这点：cookie 不会附上、Bearer token 也不能挂到
 * 原生资源请求上，于是图片 401。Desktop/移动端会暴露非空的
 * `api.getBaseUrl()`；Web 端在服务端返回的 markdown 绝对 URL 与当前页面
 * 不同源时也会走到这条路径。
 *
 * 对这类客户端：走带鉴权的 API 重新拉一次附件元数据——和点击下载时做的
 * 重签名一致——再用返回的签名 `download_url` 替换原 URL。
 *
 * 服务端只有在 CloudFront 签名或 presign 模式下才有签名 URL 可给。在
 * **proxy** 下载模式下 GetAttachmentByID 返回的还是受鉴权的 API 路径；
 * MUL-5445 之前渲染器会保留原始 URL，图片一直是坏的，元数据请求成了纯
 * 开销。proxy 并不冷门：默认 `auto` 模式只要存储 URL 指向内网主机（正是
 * docker-compose MinIO `http://minio:9000` 这种自托管形态）就会强制走
 * proxy。所以当刷新后的元数据确认没有签名 URL 时，退而求其次：用带鉴权
 * 的客户端把字节拉下来，再以对象 URL 渲染。`blobFallback` 把该方案限定在
 * 调用方确实在渲染原生 `<img>` 的场景——文件卡片只需要一个链接，为画一个
 * 小图标就把 100MB 压缩包下载进渲染进程内存，是笔亏本买卖。
 *
 * @param attachmentId 附件 id；可为空，此时尝试从 pickedUrl 反推。
 * @param pickedUrl    当前渲染器选中的内联 URL。
 * @param blobFallback 是否允许退回「下载字节 + blob URL」方案（只在真正
 *                     渲染 <img> 时才应传 true）。
 * @returns 可直接加载的图片 URL：原 URL / 新签名 URL / blob URL 之一。
 */
export function useResignedInlineMediaURL(
  attachmentId: string | undefined,
  pickedUrl: string,
  blobFallback: boolean,
): string {
  const idFromPickedUrl = attachmentIdFromDownloadURL(pickedUrl);
  const resignAttachmentId = attachmentId ?? idFromPickedUrl;
  const isCrossOriginWebURL = (() => {
    if (!/^https?:\/\//i.test(pickedUrl) || typeof window === "undefined") {
      return false;
    }
    try {
      return new URL(pickedUrl).origin !== window.location.origin;
    } catch (_err) {
      void _err;
      return false;
    }
  })();
  const needsResign =
    !!resignAttachmentId &&
    !!pickedUrl &&
    idFromPickedUrl !== undefined &&
    ((api.getBaseUrl?.() ?? "") !== "" || isCrossOriginWebURL);

  const { data: fresh } = useQuery({
    queryKey: ["attachment-inline-resign", resignAttachmentId],
    queryFn: () => api.getAttachment(resignAttachmentId as string),
    enabled: needsResign,
    staleTime: RESIGN_STALE_MS,
    gcTime: RESIGN_STALE_MS,
  });

  const dl = fresh?.download_url ?? "";
  // 只有确认是真正的升级才采用新 URL——要求绝对地址、且不再是受鉴权的
  // API 形态（即渲染器能原生加载的签名存储 URL）。
  const signedUrl =
    /^https?:\/\//i.test(dl) && attachmentIdFromDownloadURL(dl) === undefined
      ? dl
      : "";

  // 要等 `fresh` 落地后才知道该部署确实没有签名可给——提前发起字节下载
  // 会让每个 CloudFront / presign 客户端重复下载两次。
  const { data: blob } = useQuery({
    queryKey: ["attachment-inline-blob", resignAttachmentId],
    queryFn: () => api.getAttachmentBlob(resignAttachmentId as string),
    enabled: needsResign && blobFallback && !!fresh && signedUrl === "",
    staleTime: Infinity,
    gcTime: INLINE_BLOB_GC_MS,
  });
  const isAuthenticated = !!fresh;
  const blobUrl = useObjectURL(resignAttachmentId, blob, isAuthenticated);

  if (!needsResign) return pickedUrl;
  if (signedUrl) return signedUrl;
  // 只有 blob URL 属于当前附件 id、且当前 QueryClient 已持有它的鉴权元数据
  // 时才返回缓存值。这样既保留同一会话内 5 分钟的重进缓存，又防止登出或
  // 切换账号后的 QueryClient（fresh pending/rejected）在没耦合登出代码的
  // 情况下复用上一个账号的私有 blob: URL。
  if (
    blobUrl &&
    resignAttachmentId &&
    isAuthenticated &&
    blobUrlCache.get(resignAttachmentId) === blobUrl
  ) {
    return blobUrl;
  }
  if (blobUrl && !resignAttachmentId && isAuthenticated) return blobUrl;
  return pickedUrl;
}

/**
 * useObjectURL 把 Blob 转成 `blob:` URL。模块缓存让同一份 URL 在重挂载间
 * 保持稳定，这样 blob 已缓存时重进不会闪现一帧 fallback 的 pickedUrl（见
 * #7741）。引用计数 + 延迟 GC 保证最后一次卸载 5 分钟后字节被释放（与
 * INLINE_BLOB_GC_MS 一致），又不会在渲染进程退出前泄漏。
 */
function useObjectURL(
  id: string | undefined,
  blob: Blob | undefined,
  isAuthenticated: boolean,
): string {
  const [, bump] = useState(0);

  // 引用计数：重新进入时取消待执行的 GC，最后一个使用者卸载后再等
  // INLINE_BLOB_GC_MS 才 revoke + 删除。只有当前 QueryClient 持有鉴权元数据
  // 时才参与计数——否则一个登出的客户端会取消上个账号的 GC，变相延长
  // 私有字节的存活时间。
  useEffect(() => {
    if (!id || !isAuthenticated) return;
    const pending = blobUrlGCTimers.get(id);
    if (pending) {
      clearTimeout(pending);
      blobUrlGCTimers.delete(id);
    }
    blobUrlRefCount.set(id, (blobUrlRefCount.get(id) ?? 0) + 1);
    return () => {
      const cur = blobUrlRefCount.get(id) ?? 1;
      const next = cur - 1;
      if (next <= 0) {
        blobUrlRefCount.delete(id);
        const t = setTimeout(() => {
          const cached = blobUrlCache.get(id);
          if (cached) {
            try {
              URL.revokeObjectURL(cached);
            } catch (_err) {
              void _err;
            }
            blobUrlCache.delete(id);
          }
          blobUrlGCTimers.delete(id);
        }, INLINE_BLOB_GC_MS);
        if (typeof (t as unknown as { unref?: () => void }).unref === "function") {
          (t as unknown as { unref: () => void }).unref();
        }
        blobUrlGCTimers.set(id, t);
      } else {
        blobUrlRefCount.set(id, next);
      }
    };
  }, [id, isAuthenticated]);

  useEffect(() => {
    if (!blob || !id || !isAuthenticated || typeof URL.createObjectURL !== "function") return;
    if (blobUrlCache.has(id)) {
      bump((v) => v + 1);
      return;
    }
    const next = URL.createObjectURL(blob);
    blobUrlCache.set(id, next);
    bump((v) => v + 1);
  }, [blob, id, isAuthenticated]);

  if (isAuthenticated && id && blobUrlCache.has(id)) return blobUrlCache.get(id)!;
  return "";
}

/**
 * isObjectURL 判断一个 src 是否只在本渲染进程会话内有效——可以安全显示，
 * 但不能通过「复制链接」暴露或持久化到任何地方。
 */
export function isObjectURL(rawUrl: string): boolean {
  return /^blob:/i.test(rawUrl);
}
