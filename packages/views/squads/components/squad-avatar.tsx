import type { ReactNode } from "react";
import { Users } from "lucide-react";
import type { Squad } from "@multica/core/types";
import { nameInitials } from "@multica/core/workspace/actor-display";
import { resolvePublicFileUrl } from "@multica/core/workspace/avatar-url";
import { ActorAvatar as ActorAvatarBase } from "@multica/ui/components/common/actor-avatar";

export function SquadAvatar({
  squad,
  size,
  className,
  fallback,
}: {
  squad: Pick<Squad, "name" | "avatar_url">;
  size: number;
  className?: string;
  fallback?: ReactNode;
}) {
  if (!squad.avatar_url) {
    return fallback ?? (
      <div
        className="flex size-8 shrink-0 items-center justify-center rounded-md bg-muted text-muted-foreground"
        title={squad.name}
      >
        <Users className="h-4 w-4" />
      </div>
    );
  }
  return (
    <ActorAvatarBase
      name={squad.name}
      initials={nameInitials(squad.name)}
      avatarUrl={resolvePublicFileUrl(squad.avatar_url)}
      size={size}
      className={className}
    />
  );
}
