export interface InteractionQuestion {
  id: string;
  header?: string;
  question: string;
  options: { label: string; description?: string }[];
  multiSelect: boolean;
}

export interface InteractionPrompt {
  kind: string;
  title: string;
  questions: InteractionQuestion[];
}

export interface InteractionAnswer {
  decision: "allow" | "deny";
  answers?: Record<string, string[]>;
}

export interface TaskInteraction {
  id: string;
  taskId: string;
  provider: string;
  prompt: InteractionPrompt;
  status: string;
  response?: InteractionAnswer;
  createdAt: string;
  expiresAt: string;
}

export interface TaskInteractionList {
  interactions: TaskInteraction[];
  available: boolean;
}
