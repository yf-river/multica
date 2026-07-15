type SquadMemberType = "agent" | "member";
export type SquadScope = "workspace" | "personal";

export interface SquadMemberPreview {
  member_type: SquadMemberType;
  member_id: string;
}

export interface Squad {
  id: string;
  name: string;
  description: string;
  instructions: string;
  avatar_url: string | null;
  scope: SquadScope;
  leader_id: string;
  creator_id: string;
  created_at: string;
  updated_at: string;
  archived_at: string | null;
  member_count?: number;
  member_preview?: SquadMemberPreview[];
}

export interface SquadMember {
  id: string;
  member_type: SquadMemberType;
  member_id: string;
  role: string;
}

export interface CreateSquadRequest {
  name: string;
  description?: string;
  leader_id: string;
  avatar_url?: string;
  scope?: SquadScope;
  sop_profile?: Record<string, unknown>;
  members?: Array<{
    member_type: "agent" | "member";
    member_id: string;
    role?: string;
  }>;
}

export type InternalSquadTemplateKey = "user-center" | "multica-coding";

export interface EnsureInternalSquadTemplateRequest {
  template_key: InternalSquadTemplateKey;
  name?: string;
  runtime_provider?: string;
  model?: string;
  scope?: SquadScope;
}

export interface InternalSquadTemplateResponse {
  squad: Pick<Squad, "id" | "name">;
}

export type UpdateSquadRequest = Partial<Omit<CreateSquadRequest, "members">> & {
  instructions?: string;
};

// SquadMemberStatus mirrors the five-way bucket the back-end derives in
// handler/squad.go::deriveSquadMemberStatus. Kept as a string union here
// (rather than re-derived from snapshot data) so the squad page can render
// the freshest server-side judgement without re-fetching the agent
// snapshot / runtime list. `archived` wins over every runtime/task signal.
export type SquadMemberStatusValue =
  | "working"
  | "idle"
  | "offline"
  | "unstable"
  | "archived";

export interface SquadActiveIssueBrief {
  issue_id: string;
  identifier: string;
  title: string;
  issue_status: string;
}

export interface SquadMemberStatus {
  member_id: string;
  // Human members are returned with status === null so the UI can render
  // them in the same list without showing a status pill (v1 has no
  // presence signal for humans).
  status: SquadMemberStatusValue | null;
  active_issues: SquadActiveIssueBrief[];
  last_active_at: string | null;
}
