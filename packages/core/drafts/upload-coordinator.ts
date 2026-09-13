// 本文件实现模块级的「文件上传协调器」（MUL-5181，L2）。
//
// 所有权反转：上传由这里持有，而不是由发起它的 React 组件持有。编辑器只在
// 草稿里记一条持久化占位符（`DraftUpload`），把文件交给 `startUpload`；请求
// 随之活得比编辑器挂载更久。关闭编辑器/弹窗不再中止上传——结果通过
// `onSettled` 落进持久化草稿，重新打开编辑器就能看到附件。
//
// 协调器本身对草稿一无所知。`onSettled` 把结果回调给调用方，由调用方负责
// 「代际守卫」：写入前必须确认自己的草稿仍然登记着这个 `clientUploadId`
// （请求在途期间，草稿可能已被清空/提交，占位符也可能已被移除）。
//
// 中止（`abortAll`）是普通 fire-and-forget promise 做不到的一件事：登出时取消
// 所有被追踪的请求，确保上一个用户的字节绝不会在下一个会话里被绑定成附件。

import type { ApiClient } from "../api/client";
import type { Attachment } from "../types";
import { createLogger } from "../logger";

const logger = createLogger("drafts.upload-coordinator");

/** 上传发生的上下文，可选关联到某个 issue / 评论 / 聊天会话。 */
export interface UploadCoordinatorContext {
  issueId?: string;
  commentId?: string;
  chatSessionId?: string;
}

/** 一次上传的终局结果：成功带完整附件记录，失败带错误。 */
export type UploadOutcome =
  | { clientUploadId: string; status: "uploaded"; attachment: Attachment }
  | { clientUploadId: string; status: "failed"; error: Error };

/** 启动一次上传所需的参数。 */
export interface StartUploadArgs {
  /** 客户端铸造的 id，把这次请求与持久化的 `DraftUpload` 关联起来。 */
  clientUploadId: string;
  file: File;
  /** 注入 API，让协调器与框架解耦、便于单元测试。 */
  api: Pick<ApiClient, "uploadFile">;
  ctx?: UploadCoordinatorContext;
  /**
   * 终局回调。中止时不会调用——被中止的上传让占位符保持 `uploading`，
   * store 在下次加载时会丢弃它（中止发生在登出场景，届时占位符本来就会被
   * 清掉）。调用方在写入结果前 MUST 重新确认自己的草稿仍登记着
   * `clientUploadId`。
   */
  onSettled: (outcome: UploadOutcome) => void;
}

const controllers = new Map<string, AbortController>();

/**
 * 启动一次由本模块持有的上传。立即返回，结果通过 `onSettled` 送达。
 *
 * 对已在途的 `clientUploadId` 再次调用也安全（新 controller 覆盖 map 里的
 * 旧条目——调用方铸造的是唯一 id，所以实际上这只是个防御性的空操作）。
 *
 * @param args - 启动参数
 */
export function startUpload({
  clientUploadId,
  file,
  api,
  ctx,
  onSettled,
}: StartUploadArgs): void {
  const controller = new AbortController();
  controllers.set(clientUploadId, controller);

  void (async () => {
    try {
      const attachment = await api.uploadFile(
        file,
        {
          issueId: ctx?.issueId,
          commentId: ctx?.commentId,
          chatSessionId: ctx?.chatSessionId,
        },
        controller.signal,
      );
      onSettled({ clientUploadId, status: "uploaded", attachment });
    } catch (err) {
      // 中止不算失败：占位符保持原样。它在本次会话剩余时间里保持 `uploading`，
      // 下次加载时被丢弃。其他任何错误都照常上报。
      if (controller.signal.aborted || (err instanceof Error && err.name === "AbortError")) {
        logger.info("upload aborted", { clientUploadId });
        return;
      }
      onSettled({
        clientUploadId,
        status: "failed",
        error: err instanceof Error ? err : new Error("Upload failed"),
      });
    } finally {
      // 仅当该条目还是我们这条时才删除——同 id 并发重启时，我们的 finally
      // 绝不能把对方新的 controller 挤掉。
      if (controllers.get(clientUploadId) === controller) {
        controllers.delete(clientUploadId);
      }
    }
  })();
}

/** 中止一条被追踪的上传（如果存在）。 */
export function abortUpload(clientUploadId: string): void {
  const controller = controllers.get(clientUploadId);
  if (!controller) return;
  controllers.delete(clientUploadId);
  controller.abort();
}

/**
 * 中止所有被追踪的上传。登出时（清草稿之前）调用，确保在途上传不会在下一个
 * 会话里绑定成附件。
 */
export function abortAll(): void {
  if (controllers.size === 0) return;
  logger.info("aborting all uploads", { count: controllers.size });
  for (const controller of controllers.values()) {
    controller.abort();
  }
  controllers.clear();
}

/** 仅供测试：当前被追踪的上传数。 */
export function __trackedUploadCountForTest(): number {
  return controllers.size;
}
