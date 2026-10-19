"use client";

import { useEffect } from "react";
import { useNavigation } from "@multica/views/navigation";
import { useWorkspacePaths } from "@multica/core/paths";

/** Preserve existing bookmarks while retiring the personal task list. */
export default function Page() {
  const router = useNavigation();
  const paths = useWorkspacePaths();
  const destination = paths.issues();
  useEffect(() => { router.replace(destination); }, [router, destination]);
  return null;
}
