interface PollableRuntimeRequest {
  id: string;
  status: string;
}

export async function pollRuntimeRequest<T extends PollableRuntimeRequest>(
  initial: T,
  getResult: (requestId: string) => Promise<T>,
  timeoutMs: number,
  timeoutMessage: string,
): Promise<T> {
  const startedAt = Date.now();
  let current = initial;
  while (current.status === "pending" || current.status === "running") {
    if (Date.now() - startedAt > timeoutMs) throw new Error(timeoutMessage);
    await new Promise((resolve) => setTimeout(resolve, 500));
    current = await getResult(initial.id);
  }
  return current;
}
