"use client";

import { useCallback } from "react";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import { useFileUpload } from "@multica/core/hooks/use-file-upload";
import { useT } from "../i18n";

/**
 * 把 `useFileUpload` 接上上传门禁依赖的失败 toast。
 *
 * 门禁的失败兜底是「丢占位符、说明原因、让用户再传一次」。丢占位符由上传
 * 扩展处理，但没有 toast 的话文件会在上传中途无解释地消失——这正是所有
 * composer 之前的状况：`uploadWithToast` 只在调用方提供 `onError` 时才弹
 * toast，而从没调用方提供过（MUL-4808）。
 *
 * `useFileUpload` 住在 `@multica/core`，它不能 import UI 库，所以 toast 在
 * 这里喂给它——一次，而不是每个 composer 各一份。
 */
function useEditorUpload() {
  const { t } = useT("editor");
  const onError = useCallback(
    (error: Error, file: File) => {
      toast.error(
        t(($) => $.upload.failed, { filename: file.name, reason: error.message }),
      );
    },
    [t],
  );
  return useFileUpload(api, onError);
}

/** 编辑器上传 hook：失败自动弹 toast 提示。 */
export { useEditorUpload };
