"use client";
// 本文件是统一头像组件：无论成员、Agent、Squad 还是系统，都用同一个圆形头像渲染。
// 有头像图就显示图，没有图时按「emoji → 系统/Agent/Squad 图标 → 姓名首字母」逐级兜底。

import { useState, useEffect } from "react";
import { Bot, Users } from "lucide-react";
import { cn } from "@multica/ui/lib/utils";
import {
  AVATAR_SIZE_PX,
  DEFAULT_AVATAR_SIZE,
  type AvatarSize,
} from "@multica/ui/lib/avatar-size";
import { parseAvatarEmoji } from "@multica/ui/lib/avatar-emoji";
import { MulticaIcon } from "./multica-icon";

/**
 * ActorAvatar 组件的 props 定义。
 */
interface ActorAvatarProps {
  /** 头像对应的名字，同时用作图片的 alt 和无图时图标的可读标签 */
  name: string;
  /** 姓名首字母缩写，在无头像图、无 emoji、非系统/Agent/Squad 时显示 */
  initials: string;
  /** 头像图片地址；可以是普通图片 URL，也可以是 `emoji:` 前缀的 emoji 形式 */
  avatarUrl?: string | null;
  /** 是否为 Agent（无头像图时显示机器人图标兜底） */
  isAgent?: boolean;
  /** 是否为系统（无头像图时显示 Multica 图标兜底） */
  isSystem?: boolean;
  /** 是否为 Squad（无头像图时显示群组图标兜底） */
  isSquad?: boolean;
  /** 头像尺寸档位，默认取 avatar-size 里定义的默认档 */
  size?: AvatarSize;
  /** 额外的外层 class 名 */
  className?: string;
}

/**
 * ActorAvatar：所有角色（成员 / Agent / Squad / 系统）的圆形头像。
 *
 * 头像形状统一为圆形，由本组件作为唯一事实来源；上传编辑器里也保持同一形态
 * （见 packages/views/common/avatar-upload-control.tsx）。
 *
 * 兜底顺序：emoji 头像 → 图片 →（系统图标 / 机器人图标 / 群组图标）→ 姓名首字母。
 * 图片加载失败时自动降级到图标或首字母，换地址后会重置错误状态重新尝试。
 *
 * @param props 见 ActorAvatarProps 定义
 * @returns 圆形头像元素
 */
function ActorAvatar({
  name,
  initials,
  avatarUrl,
  isAgent,
  isSystem,
  isSquad,
  size = DEFAULT_AVATAR_SIZE,
  className,
}: ActorAvatarProps) {
  const [imgError, setImgError] = useState(false);
  const px = AVATAR_SIZE_PX[size];
  const emoji = parseAvatarEmoji(avatarUrl);

  // 头像地址变化时把错误态清零，让新地址能重新尝试加载
  useEffect(() => {
    setImgError(false);
  }, [avatarUrl]);

  // 所有角色——成员、Agent、Squad、系统——都渲染成圆形。这是头像形状的
  // 唯一事实来源；上传编辑器也保持同一形态（packages/views/common/avatar-upload-control.tsx）。
  return (
    <div
      data-slot="avatar"
      className={cn(
        "inline-flex shrink-0 items-center justify-center font-medium overflow-hidden",
        (!avatarUrl || emoji || imgError) && "bg-muted text-muted-foreground",
        className,
        // rounded-full 必须放在最后：这样调用方的 className 永远覆盖不了圆形，
        // 头像必须是圆是硬性约定，而不是每个调用方可以自行决定的事。
        "rounded-full"
      )}
      style={{ width: px, height: px, fontSize: px * 0.45 }}
    >
      {emoji ? (
        <span
          role="img"
          aria-label={name}
          className="select-none leading-none"
          style={{ fontSize: px * 0.58 }}
        >
          {emoji}
        </span>
      ) : avatarUrl && !imgError ? (
        <img
          src={avatarUrl}
          alt={name}
          className="h-full w-full object-cover"
          onError={() => setImgError(true)}
        />
      ) : isSystem ? (
        <MulticaIcon noSpin style={{ width: px * 0.55, height: px * 0.55 }} />
      ) : isAgent ? (
        <Bot style={{ width: px * 0.55, height: px * 0.55 }} />
      ) : isSquad ? (
        <Users style={{ width: px * 0.55, height: px * 0.55 }} />
      ) : (
        initials
      )}
    </div>
  );
}

export { ActorAvatar, type ActorAvatarProps };
