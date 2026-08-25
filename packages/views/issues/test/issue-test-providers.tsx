import type { ReactElement, ReactNode } from "react";
import { render, type RenderResult } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { TestI18nProvider } from "../../test/i18n";

export function createIssueTestQueryClient() {
  return new QueryClient({
    defaultOptions: {
      queries: { retry: false, gcTime: 0 },
      mutations: { retry: false },
    },
  });
}

export function IssueTestProviders({
  children,
  queryClient = createIssueTestQueryClient(),
}: {
  children: ReactNode;
  queryClient?: QueryClient;
}) {
  return (
    <TestI18nProvider>
      <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
    </TestI18nProvider>
  );
}

export function renderIssueTest(
  ui: ReactElement,
  queryClient = createIssueTestQueryClient(),
): RenderResult & { queryClient: QueryClient } {
  return {
    ...render(<IssueTestProviders queryClient={queryClient}>{ui}</IssueTestProviders>),
    queryClient,
  };
}
