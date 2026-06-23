import { PromptLibraryPage } from "./prompt-library-page";

export function PromptPlaygroundPage() {
  return <PromptLibraryPage activeView="prompt-playground" surface="prompt-playground" />;
}

export function AgentPlaygroundPage() {
  return <PromptLibraryPage activeView="agent-playground" showPromptEditor={false} surface="agent-playground" />;
}
