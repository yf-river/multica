// Runtime reachability and current workload are independent UI dimensions.
export type AgentAvailability =
  | "online"
  | "unstable"
  | "offline"
  | "archived";

export type Workload =
  | "working"
  | "queued"
  | "idle";

export interface AgentPresenceDetail {
  availability: AgentAvailability;
  workload: Workload;
  runningCount: number;
  queuedCount: number;
}
