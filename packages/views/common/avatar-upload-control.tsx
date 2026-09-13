// 头像上传控件：点击后选择图片、裁剪并上传，支持用 emoji 替代图片；上传与持久化职责分离，由调用方保存。
"use client";

import { Suspense, lazy, useRef, useState } from "react";
import { Bot, Camera, ImagePlus, Loader2, Users, X } from "lucide-react";
import { toast } from "sonner";
import { api } from "@multica/core/api";
import { useFileUpload } from "@multica/core/hooks/use-file-upload";
import { resolvePublicFileUrl } from "@multica/core/workspace/avatar-url";
import {
  AVATAR_EMOJI_SUGGESTIONS,
  formatAvatarEmoji,
  parseAvatarEmoji,
} from "@multica/ui/lib/avatar-emoji";
import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@multica/ui/components/ui/popover";
import { Separator } from "@multica/ui/components/ui/separator";
import { cn } from "@multica/ui/lib/utils";
import { useT } from "../i18n";
import { AvatarCropDialog } from "./avatar-crop-dialog";

// 完整的 emoji-mart 选择器约有 1MB 的 emoji 数据。只有少量建议 emoji 立即渲染，
// 可搜索的完整集合在需要时才加载。
const EmojiPicker = lazy(() =>
  import("@multica/ui/components/common/emoji-picker").then((m) => ({
    default: m.EmojiPicker,
  })),
);

/** 头像上传控件的适用对象类型：用户 / 智能体 / 小队 / 工作区。 */
export type AvatarUploadVariant = "user" | "agent" | "squad" | "workspace";

interface AvatarUploadControlProps {
  /** 当前头像 URL（原始、未解析）。`null` 渲染空状态。 */
  value: string | null;
  /** 决定空状态的兜底图标/首字母。 */
  variant: AvatarUploadVariant;
  /** 用于首字母/首字符兜底和图片 alt 的名称。 */
  name?: string;
  /** 圆形头像的直径（像素），默认 64。 */
  size?: number;
  /** 是否禁用。 */
  disabled?: boolean;
  /**
   * 裁剪并上传成功后携带文件 URL 触发。父级负责持久化（updateMe /
   * updateWorkspace / updateAgent / updateSquad，或暂存用于创建调用）。
   * 裁剪对话框会保持忙碌状态直到此回调完成，然后关闭。
   */
  onUploaded: (url: string) => void | Promise<unknown>;
  /**
   * 提供后，头像允许用 emoji 替代图片：点击会打开一个选择器，含建议集合、
   * 完整 emoji 搜索和上传入口。以要持久化的值触发（如 `emoji:🚀`），与
   * `onUploaded` 的 `avatar_url` 形状一致，调用方用相同方式存储。省略时
   * 控件保持"点击直接上传"的行为。
   */
  onEmojiSelected?: (value: string) => void | Promise<unknown>;
  /**
   * 提供后，显示一个小型清除按钮。用于创建流程丢弃尚未持久化的选择；编辑
   * 流程省略它（移除已保存头像不在范围内）。
   */
  onClear?: () => void;
  /** 附加样式。 */
  className?: string;
  /** 自定义 aria-label。 */
  ariaLabel?: string;
}

function initialsOf(name: string): string {
  return name
    .split(" ")
    .map((word) => word[0])
    .join("")
    .toUpperCase()
    .slice(0, 2);
}

function AvatarFallback({
  variant,
  name,
  size,
}: {
  variant: AvatarUploadVariant;
  name: string;
  size: number;
}) {
  if (variant === "agent") {
    return <Bot style={{ width: size * 0.5, height: size * 0.5 }} />;
  }
  if (variant === "squad") {
    return <Users style={{ width: size * 0.5, height: size * 0.5 }} />;
  }
  const text =
    variant === "workspace"
      ? name.charAt(0).toUpperCase()
      : initialsOf(name);
  return (
    <span className="font-semibold" style={{ fontSize: size * 0.4 }}>
      {text}
    </span>
  );
}

/**
 * 执行调用方的持久化回调，并报告是否成功。
 *
 * 故意吞掉拒绝而不是弹 toast：执行写入的调用方已经负责了那份反馈。
 * `AgentDetailPage.handleUpdate` 会弹 toast 再重新抛出，好让自动保存渲染失败
 * 状态；设置页签各自弹自己的 toast；创建流程只是把值暂存在本地状态，根本不会
 * 失败。在这里再报告一次就会把同一个失败弹两遍。而本控件自己执行的上传没有
 * 其他负责人，所以那一个仍然要提示。
 */
async function persistedByCaller(
  save: () => void | Promise<unknown>,
): Promise<boolean> {
  try {
    await save();
    return true;
  } catch {
    return false;
  }
}

/**
 * 供 web/desktop 共用的"点击上传"头像控件。渲染当前头像并带悬停"更换"提示；
 * 选中图片后打开 {@link AvatarCropDialog} 进行重定位/缩放，再通过现有的
 * `/api/upload-file` 链路上传裁剪后的图片，并通过 `onUploaded` 把 URL 交回。
 * 提供 `onEmojiSelected` 时，点击会打开一个改用 emoji 的选择器——两条路径
 * 都产出 `avatar_url` 值。业务持久化由调用方负责。
 * @param value 当前头像 URL。
 * @param variant 头像对象类型。
 * @param name 名称（用于首字母兜底与 alt）。
 * @param size 直径（像素）。
 * @param disabled 是否禁用。
 * @param onUploaded 上传成功回调。
 * @param onEmojiSelected 选择 emoji 回调。
 * @param onClear 清除回调。
 * @param className 附加样式。
 * @param ariaLabel 自定义 aria-label。
 */
export function AvatarUploadControl({
  value,
  variant,
  name = "",
  size = 64,
  disabled = false,
  onUploaded,
  onEmojiSelected,
  onClear,
  className,
  ariaLabel,
}: AvatarUploadControlProps) {
  const { t } = useT("common");
  const { upload } = useFileUpload(api);
  const fileInputRef = useRef<HTMLInputElement>(null);
  const [pickedFile, setPickedFile] = useState<File | null>(null);
  const [dialogOpen, setDialogOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [previewError, setPreviewError] = useState(false);
  const [pickerOpen, setPickerOpen] = useState(false);
  const [emojiSearchOpen, setEmojiSearchOpen] = useState(false);

  const emoji = parseAvatarEmoji(value);
  const resolved = value && !emoji ? resolvePublicFileUrl(value) : null;
  const hasImage = !!resolved && !previewError;
  const hasAvatar = !!emoji || hasImage;
  const emojiEnabled = !!onEmojiSelected;

  const openFileDialog = () => fileInputRef.current?.click();

  const closePicker = () => {
    setPickerOpen(false);
    setEmojiSearchOpen(false);
  };

  const handlePickerOpenChange = (next: boolean) => {
    setPickerOpen(next);
    if (!next) setEmojiSearchOpen(false);
  };

  const handlePick = (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    e.target.value = ""; // 清空，允许再次选择同一个文件
    if (!file) return;
    if (!file.type.startsWith("image/")) {
      toast.error(t(($) => $.avatar_upload.select_image));
      return;
    }
    setPreviewError(false);
    setPickedFile(file);
    setDialogOpen(true);
  };

  const handleCropped = async (cropped: File) => {
    setBusy(true);
    try {
      const result = await upload(cropped);
      if (!result) return;
      if (!(await persistedByCaller(() => onUploaded(result.link)))) return;
      setDialogOpen(false);
      setPickedFile(null);
      toast.success(t(($) => $.avatar_upload.updated));
    } catch (err) {
      // 只有上面的上传才会走到这里——见 persistedByCaller。
      toast.error(
        err instanceof Error ? err.message : t(($) => $.avatar_upload.failed),
      );
    } finally {
      setBusy(false);
    }
  };

  // 整个保存过程都保持忙碌，和上传一样：编辑型调用方每次选择都会 PATCH，
  // 若触发器仍可用，第二次选择会和第一次竞争。两次写入在服务端都是
  // "后到者胜"，输掉的可能是用户最后选的那个——这样头像就会永久错误，
  // 因为随后的失效刷新只会收敛到服务端最终保留的值。
  //
  // 故意不弹成功 toast：头像会就地换成所选 emoji，变化已经可见。
  const handleEmojiSelected = async (picked: string) => {
    closePicker();
    setPreviewError(false);
    setBusy(true);
    try {
      await persistedByCaller(() =>
        onEmojiSelected?.(formatAvatarEmoji(picked)),
      );
    } finally {
      setBusy(false);
    }
  };

  const avatarButton = (
    <button
      type="button"
      // 启用 emoji 时，这个按钮是弹层触发器，点击处理器由 Base UI 提供；
      // 未启用时点击直接打开文件对话框，这是本控件最初单一用途的形态。
      onClick={emojiEnabled ? undefined : openFileDialog}
      disabled={disabled || busy}
      aria-label={ariaLabel ?? t(($) => $.avatar_upload.change)}
      className={cn(
        "group relative h-full w-full overflow-hidden bg-muted text-muted-foreground outline-none",
        "flex items-center justify-center",
        "focus-visible:ring-2 focus-visible:ring-ring",
        "disabled:cursor-not-allowed disabled:opacity-60",
        "rounded-full",
        className,
      )}
      style={{ width: size, height: size }}
    >
      {emoji ? (
        <span
          role="img"
          aria-label={name}
          className="select-none leading-none"
          style={{ fontSize: size * 0.58 }}
        >
          {emoji}
        </span>
      ) : hasImage ? (
        <img
          src={resolved ?? undefined}
          alt={name}
          className="h-full w-full object-cover"
          onError={() => setPreviewError(true)}
        />
      ) : (
        <AvatarFallback variant={variant} name={name} size={size} />
      )}

      {!disabled && (
        <div className="absolute inset-0 flex items-center justify-center bg-black/40 opacity-0 transition-opacity group-hover:opacity-100">
          {busy ? (
            <Loader2 className="h-5 w-5 animate-spin text-white" />
          ) : (
            <Camera className="h-5 w-5 text-white" />
          )}
        </div>
      )}
    </button>
  );

  return (
    <div className="relative shrink-0" style={{ width: size, height: size }}>
      {emojiEnabled ? (
        <Popover open={pickerOpen} onOpenChange={handlePickerOpenChange}>
          <PopoverTrigger render={avatarButton} />
          <PopoverContent
            align="start"
            // emoji-mart 选择器会自己定尺寸；只有建议网格按弹层自身宽度布局。
            className={cn("gap-0 p-0", emojiSearchOpen ? "w-auto" : "w-72")}
          >
            {emojiSearchOpen ? (
              <Suspense
                fallback={
                  <div className="p-4 text-body text-muted-foreground">
                    {t(($) => $.avatar_upload.emoji_loading)}
                  </div>
                }
              >
                <EmojiPicker onSelect={handleEmojiSelected} />
              </Suspense>
            ) : (
              <>
                <div className="p-1">
                  <button
                    type="button"
                    onClick={() => {
                      closePicker();
                      openFileDialog();
                    }}
                    className="flex w-full items-center gap-1.5 rounded-md px-1.5 py-1.5 text-body outline-hidden transition-colors hover:bg-accent hover:text-accent-foreground focus-visible:bg-accent focus-visible:text-accent-foreground"
                  >
                    <ImagePlus className="size-4 shrink-0 text-muted-foreground" />
                    {t(($) => $.avatar_upload.upload_image)}
                  </button>
                </div>

                <Separator />

                <div className="p-1">
                  <p className="px-1.5 py-1 text-caption font-medium text-muted-foreground">
                    {t(($) => $.avatar_upload.emoji_label)}
                  </p>
                  <div className="grid grid-cols-8 gap-0.5">
                    {AVATAR_EMOJI_SUGGESTIONS.map((suggestion) => (
                      <button
                        key={suggestion}
                        type="button"
                        aria-pressed={emoji === suggestion}
                        onClick={() => handleEmojiSelected(suggestion)}
                        className={cn(
                          "flex h-8 w-8 items-center justify-center rounded-md text-title-sm leading-none outline-hidden transition-colors hover:bg-accent focus-visible:ring-2 focus-visible:ring-ring",
                          emoji === suggestion && "bg-accent ring-1 ring-ring",
                        )}
                      >
                        {suggestion}
                      </button>
                    ))}
                  </div>
                  <button
                    type="button"
                    onClick={() => setEmojiSearchOpen(true)}
                    className="mt-1 w-full rounded-md px-1.5 py-1 text-caption text-muted-foreground outline-hidden transition-colors hover:bg-accent hover:text-foreground focus-visible:bg-accent focus-visible:text-foreground"
                  >
                    {t(($) => $.avatar_upload.more_emojis)}
                  </button>
                </div>
              </>
            )}
          </PopoverContent>
        </Popover>
      ) : (
        avatarButton
      )}

      {onClear && hasAvatar && !busy && !disabled && (
        <button
          type="button"
          onClick={(e) => {
            e.stopPropagation();
            setPreviewError(false);
            onClear();
          }}
          className="absolute -right-1.5 -top-1.5 flex h-5 w-5 items-center justify-center rounded-full border bg-background text-muted-foreground shadow-sm transition-colors hover:bg-muted hover:text-foreground"
          aria-label={t(($) => $.avatar_upload.remove)}
        >
          <X className="h-3 w-3" />
        </button>
      )}

      <input
        ref={fileInputRef}
        type="file"
        accept="image/*"
        className="hidden"
        onChange={handlePick}
      />

      <AvatarCropDialog
        file={pickedFile}
        open={dialogOpen}
        busy={busy}
        onOpenChange={(next) => {
          if (busy) return;
          setDialogOpen(next);
          if (!next) setPickedFile(null);
        }}
        onCropped={handleCropped}
      />
    </div>
  );
}
