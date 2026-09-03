import { useParams } from "react-router-dom";
import { useQuery } from "@tanstack/react-query";
import {
  RuntimeDetailPage as SharedRuntimeDetailPage,
  RuntimeSettingsPage as SharedRuntimeSettingsPage,
} from "@multica/views/runtimes";
import { useWorkspaceId } from "@multica/core/hooks";
import { runtimeDisplayLabel } from "@multica/core/runtimes";
import { runtimeListOptions } from "@multica/core/runtimes/queries";
import { useDocumentTitle } from "@/hooks/use-document-title";

export function RuntimeDetailPage() {
  const { id } = useParams<{ id: string }>();
  const wsId = useWorkspaceId();
  const { data: runtimes } = useQuery(runtimeListOptions(wsId));
  const runtime = runtimes?.find((candidate) => candidate.id === id);

  useDocumentTitle(runtime ? runtimeDisplayLabel(runtime) : "Runtimes");

  if (!id) return null;
  return (
    <SharedRuntimeDetailPage
      runtimeId={id}
    />
  );
}

export function RuntimeSettingsPage() {
  const { id, runtimeId } = useParams<{
    id: string;
    runtimeId: string;
  }>();
  const wsId = useWorkspaceId();
  const { data: runtimes } = useQuery(runtimeListOptions(wsId));
  const runtime = runtimes?.find((candidate) => candidate.id === runtimeId);

  useDocumentTitle(runtime ? runtimeDisplayLabel(runtime) : "Runtime");

  if (!id || !runtimeId) return null;
  return <SharedRuntimeSettingsPage machineId={id} runtimeId={runtimeId} />;
}
