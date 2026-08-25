"use client";

import { ActorAvatar as ActorAvatarBase } from "@multica/ui/components/common/actor-avatar";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { resolvePublicFileUrl } from "@multica/core/workspace/avatar-url";
import { nameInitials } from "@multica/core/workspace/actor-display";

export function AgentCardLoadingState() {
  return (
    <div className="flex items-center gap-3">
      <Skeleton className="h-10 w-10 rounded-full" />
      <div className="flex-1 space-y-1.5">
        <Skeleton className="h-4 w-28" />
        <Skeleton className="h-3 w-20" />
      </div>
    </div>
  );
}

export function AgentCardUnavailable({ label }: { label: string }) {
  return <div className="text-xs text-muted-foreground">{label}</div>;
}

export function AgentCardAvatar({
  name,
  avatarUrl,
}: {
  name: string;
  avatarUrl: string | null;
}) {
  return (
    <ActorAvatarBase
      name={name}
      initials={nameInitials(name)}
      avatarUrl={resolvePublicFileUrl(avatarUrl)}
      isAgent
      size={40}
      className="rounded-md"
    />
  );
}
