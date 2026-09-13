// 头像裁剪对话框：对选中的图片进行拖拽/缩放/旋转，套在圆形裁剪框内预览，确认后输出固定尺寸的方形头像文件。
"use client";

import { useEffect, useState } from "react";
import Cropper, { type Area, type Point } from "react-easy-crop";
import { Loader2, Minus, Plus, RotateCw } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Slider } from "@multica/ui/components/ui/slider";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { useT } from "../i18n";
import {
  AVATAR_OUTPUT_SIZE,
  blobToAvatarFile,
  getCroppedAvatarBlob,
  pickOutputType,
  type PixelCrop,
} from "./avatar-crop";

const MIN_ZOOM = 1;
const MAX_ZOOM = 3;
const ZOOM_STEP = 0.1;

interface AvatarCropDialogProps {
  /** 选中的源文件，`null` 时对话框保持空白。 */
  file: File | null;
  /** 是否打开对话框。 */
  open: boolean;
  /** 父级的上传/保存正在进行中——锁定控件并阻止关闭。 */
  busy?: boolean;
  /** 打开状态变化回调。 */
  onOpenChange: (open: boolean) => void;
  /** 裁剪并压缩完成后触发，父级负责上传和持久化。 */
  onCropped: (file: File) => void;
}

/**
 * 头像裁剪器。图片在固定的圆形裁剪窗口下方平移/缩放/旋转，窗口外的区域
 * 变暗。输出是一张 {@link AVATAR_OUTPUT_SIZE} 像素的方形图片——圆形遮罩只是
 * 展示效果，绝不会写入像素。
 *
 * 交互（拖拽 / 缩放 / 旋转 + 变暗遮罩）委托给 react-easy-crop；本组件只负责
 * 外框（标题、缩放滑杆、旋转/重置控件）和编码/上传的交接。
 * @param file 要裁剪的源文件。
 * @param open 是否打开。
 * @param busy 上传中标识，锁控件并阻止关闭。
 * @param onOpenChange 打开状态变化回调。
 * @param onCropped 裁剪完成回调。
 */
export function AvatarCropDialog({
  file,
  open,
  busy = false,
  onOpenChange,
  onCropped,
}: AvatarCropDialogProps) {
  const { t } = useT("common");
  const [objectUrl, setObjectUrl] = useState<string | null>(null);
  const [crop, setCrop] = useState<Point>({ x: 0, y: 0 });
  const [zoom, setZoom] = useState(MIN_ZOOM);
  const [rotation, setRotation] = useState(0);
  const [croppedAreaPixels, setCroppedAreaPixels] = useState<PixelCrop | null>(
    null,
  );
  const [loadError, setLoadError] = useState(false);

  // 每个选中文件对应一个 object URL，在对话框存续期间有效；重置变换，
  // 让新选中的图片从居中、未旋转的状态开始。
  useEffect(() => {
    if (!file) {
      setObjectUrl(null);
      return;
    }
    const url = URL.createObjectURL(file);
    setObjectUrl(url);
    setCrop({ x: 0, y: 0 });
    setZoom(MIN_ZOOM);
    setRotation(0);
    setCroppedAreaPixels(null);
    setLoadError(false);
    return () => URL.revokeObjectURL(url);
  }, [file]);

  const clampZoom = (value: number) =>
    Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, Math.round(value * 100) / 100));

  const resetTransform = () => {
    setCrop({ x: 0, y: 0 });
    setZoom(MIN_ZOOM);
    setRotation(0);
  };

  const handleConfirm = async () => {
    if (!objectUrl || !croppedAreaPixels || !file) return;
    const { type, quality } = pickOutputType();
    try {
      const blob = await getCroppedAvatarBlob(objectUrl, croppedAreaPixels, rotation, {
        output: AVATAR_OUTPUT_SIZE,
        type,
        quality,
        // JPEG 没有透明通道；给透明/旋转后的角落铺白色底，而不是画布默认的黑色。
        background: type === "image/jpeg" ? "#ffffff" : undefined,
      });
      onCropped(blobToAvatarFile(blob, file.name, type));
    } catch {
      setLoadError(true);
    }
  };

  const disabled = busy || !objectUrl || loadError || !croppedAreaPixels;

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        // 上传提交期间绝不允许关闭对话框。
        if (busy) return;
        onOpenChange(next);
      }}
    >
      <DialogContent showCloseButton={!busy} className="sm:max-w-md">
        <DialogHeader>
          <DialogTitle>{t(($) => $.avatar_crop.title)}</DialogTitle>
        </DialogHeader>

        <div className="relative aspect-square w-full overflow-hidden rounded-lg bg-neutral-900">
          {objectUrl && !loadError ? (
            <>
              <Cropper
                image={objectUrl}
                crop={crop}
                zoom={zoom}
                rotation={rotation}
                aspect={1}
                minZoom={MIN_ZOOM}
                maxZoom={MAX_ZOOM}
                cropShape="round"
                showGrid={false}
                onCropChange={setCrop}
                onZoomChange={setZoom}
                onRotationChange={setRotation}
                onCropComplete={(_: Area, areaPixels: Area) =>
                  setCroppedAreaPixels(areaPixels)
                }
              />
              <button
                type="button"
                onClick={() => setRotation((r) => (r + 90) % 360)}
                disabled={busy}
                aria-label={t(($) => $.avatar_crop.rotate)}
                className="absolute right-2 top-2 z-10 flex h-8 w-8 items-center justify-center rounded-full bg-background/90 text-foreground shadow-sm transition-colors hover:bg-background disabled:opacity-50"
              >
                <RotateCw className="h-4 w-4" />
              </button>
            </>
          ) : (
            <div className="flex h-full w-full items-center justify-center text-caption text-muted-foreground">
              {loadError
                ? t(($) => $.avatar_crop.load_failed)
                : t(($) => $.avatar_crop.loading)}
            </div>
          )}
        </div>

        <div className="flex items-center gap-3">
          <button
            type="button"
            onClick={() => setZoom((z) => clampZoom(z - ZOOM_STEP))}
            disabled={disabled}
            aria-label={t(($) => $.avatar_crop.zoom_out)}
            className="shrink-0 text-muted-foreground transition-colors hover:text-foreground disabled:opacity-40"
          >
            <Minus className="h-4 w-4" />
          </button>
          <Slider
            value={[zoom]}
            min={MIN_ZOOM}
            max={MAX_ZOOM}
            step={0.01}
            disabled={disabled}
            onValueChange={(value) =>
              setZoom((Array.isArray(value) ? value[0] : value) ?? MIN_ZOOM)
            }
            aria-label={t(($) => $.avatar_crop.zoom)}
            className="flex-1"
          />
          <button
            type="button"
            onClick={() => setZoom((z) => clampZoom(z + ZOOM_STEP))}
            disabled={disabled}
            aria-label={t(($) => $.avatar_crop.zoom_in)}
            className="shrink-0 text-muted-foreground transition-colors hover:text-foreground disabled:opacity-40"
          >
            <Plus className="h-4 w-4" />
          </button>
        </div>

        <div className="flex items-center justify-between">
          <Button variant="ghost" onClick={resetTransform} disabled={busy}>
            {t(($) => $.avatar_crop.reset)}
          </Button>
          <div className="flex gap-2">
            <Button
              variant="outline"
              onClick={() => onOpenChange(false)}
              disabled={busy}
            >
              {t(($) => $.avatar_crop.cancel)}
            </Button>
            <Button onClick={handleConfirm} disabled={disabled}>
              {busy ? (
                <>
                  <Loader2 className="h-3.5 w-3.5 animate-spin" />
                  {t(($) => $.avatar_crop.uploading)}
                </>
              ) : (
                t(($) => $.avatar_crop.apply)
              )}
            </Button>
          </div>
        </div>
      </DialogContent>
    </Dialog>
  );
}
