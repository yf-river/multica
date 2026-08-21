"use client";

import { useRef, useState } from "react";
import { toast } from "sonner";
import { useCreateWorkspace } from "@multica/core/workspace/mutations";
import type { Workspace } from "@multica/core/types";
import { isReservedSlug } from "@multica/core/paths";
import {
  WORKSPACE_SLUG_REGEX,
  isWorkspaceSlugConflict,
  nameToWorkspaceSlug,
} from "./slug";

type WorkspaceCreateMessages = {
  slugFormat: string;
  slugReserved: string;
  slugTaken: string;
  slugConflictToast: string;
  createFailed: string;
};

type WorkspaceCreateControllerOptions = {
  onSuccess: (workspace: Workspace) => void | Promise<void>;
  messages: WorkspaceCreateMessages;
  preventSubmitWhilePending?: boolean;
};

export function useWorkspaceCreateController({
  onSuccess,
  messages,
  preventSubmitWhilePending = false,
}: WorkspaceCreateControllerOptions) {
  const createWorkspace = useCreateWorkspace();
  const [name, setName] = useState("");
  const [slug, setSlug] = useState("");
  const [slugServerError, setSlugServerError] = useState<string | null>(null);
  const slugTouched = useRef(false);

  const slugValidationError =
    slug.length > 0 && !WORKSPACE_SLUG_REGEX.test(slug)
      ? messages.slugFormat
      : null;
  const slugReservedError =
    slug.length > 0 && isReservedSlug(slug) ? messages.slugReserved : null;
  const slugError = slugValidationError ?? slugReservedError ?? slugServerError;
  const canSubmit =
    name.trim().length > 0 && slug.trim().length > 0 && !slugError;

  const handleNameChange = (value: string) => {
    setName(value);
    if (!slugTouched.current) {
      setSlug(nameToWorkspaceSlug(value));
      setSlugServerError(null);
    }
  };

  const handleSlugChange = (value: string) => {
    slugTouched.current = true;
    setSlug(value);
    setSlugServerError(null);
  };

  const handleCreate = () => {
    if (!canSubmit || (preventSubmitWhilePending && createWorkspace.isPending)) {
      return;
    }
    createWorkspace.mutate(
      { name: name.trim(), slug: slug.trim() },
      {
        onSuccess,
        onError: (error) => {
          if (isWorkspaceSlugConflict(error)) {
            setSlugServerError(messages.slugTaken);
            toast.error(messages.slugConflictToast);
            return;
          }
          toast.error(
            error instanceof Error && error.message
              ? error.message
              : messages.createFailed,
          );
        },
      },
    );
  };

  return {
    name,
    slug,
    slugError,
    canSubmit,
    isPending: createWorkspace.isPending,
    handleNameChange,
    handleSlugChange,
    handleCreate,
  };
}
