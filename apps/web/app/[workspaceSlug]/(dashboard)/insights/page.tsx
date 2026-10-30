"use client";
import {useWorkspaceId} from '@multica/core/hooks';
import {InsightsPage} from '@multica/views/insights';
export default function Page(){return <InsightsPage workspaceId={useWorkspaceId()}/>;}
