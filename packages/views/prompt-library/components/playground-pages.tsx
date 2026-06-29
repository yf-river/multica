import { AgentPlaygroundContainer, DebugRunsContainer, PromptPlaygroundContainer } from "./playground-containers";

export function PromptPlaygroundPage() {
  return <PromptPlaygroundContainer />;
}

export function AgentPlaygroundPage() {
  return <AgentPlaygroundContainer />;
}

export function DebugRunsPage() {
  return <DebugRunsContainer />;
}
