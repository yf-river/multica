"use client";
// 本文件是「附件上传」按钮：一个回形针图标按钮，点击后触发隐藏的文件选择框，
// 选中文件后把 File 逐个交给调用方（由调用方负责真正上传），本组件不做任何上传逻辑。

import { useRef } from "react";
import { Paperclip } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "@multica/ui/components/ui/button";
import { cn } from "@multica/ui/lib/utils";

/**
 * FileUploadButton 组件的 props 定义。
 */
interface FileUploadButtonProps {
  /** 选中文件后的回调，参数为选中的 File——上传由调用方处理 */
  onSelect: (file: File) => void;
  /** 禁用按钮 */
  disabled?: boolean;
  /** 额外的外层 class 名 */
  className?: string;
  /** 按钮尺寸：'sm' 小图标 / 'default' 常规 */
  size?: "sm" | "default";
  /** 是否允许多选文件 */
  multiple?: boolean;
}

/**
 * FileUploadButton：隐藏 input[type=file] + 回形针触发按钮的组合。
 * 每次选择后立即清空 input 的 value，保证用户再次选择同一个文件也能触发 onChange。
 *
 * @param props 见 FileUploadButtonProps 定义
 * @returns 触发按钮与隐藏文件输入框的片段
 */
function FileUploadButton({
  onSelect,
  disabled,
  className,
  size = "default",
  multiple = false,
}: FileUploadButtonProps) {
  const { t } = useTranslation("ui");
  const inputRef = useRef<HTMLInputElement>(null);
  const attachLabel = t(($) => $.attach_file);

  const handleChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    const files = Array.from(e.target.files ?? []);
    if (files.length === 0) return;
    // 先清空再逐个回调：下次选同一文件时 input 状态是干净的，能再次触发 onChange
    e.target.value = "";
    for (const file of files) onSelect(file);
  };

  const iconSize = size === "sm" ? "h-3.5 w-3.5" : "h-4 w-4";
  const buttonSize = size === "sm" ? "icon-xs" : "icon-sm";

  return (
    <>
      <Button
        type="button"
        variant="ghost"
        size={buttonSize}
        onClick={() => inputRef.current?.click()}
        disabled={disabled}
        aria-label={attachLabel}
        title={attachLabel}
        className={cn("text-muted-foreground", className)}
      >
        <Paperclip className={iconSize} />
      </Button>
      <input
        ref={inputRef}
        type="file"
        multiple={multiple}
        className="hidden"
        onChange={handleChange}
      />
    </>
  );
}

export { FileUploadButton, type FileUploadButtonProps };
