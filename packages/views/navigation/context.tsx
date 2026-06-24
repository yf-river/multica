"use client";

import { createContext, use } from "react";
import type { NavigationAdapter } from "./types";

const NavigationContext = createContext<NavigationAdapter | null>(null);
const NavigationPendingContext = createContext<boolean>(false);

export function NavigationProvider({
  value,
  children,
}: {
  value: NavigationAdapter;
  children: React.ReactNode;
}) {
  return (
    <NavigationContext.Provider value={value}>
      <NavigationPendingContext.Provider value={false}>
        {children}
      </NavigationPendingContext.Provider>
    </NavigationContext.Provider>
  );
}

export function useNavigation(): NavigationAdapter {
  const ctx = use(NavigationContext);
  if (!ctx)
    throw new Error("useNavigation must be used within NavigationProvider");
  return ctx;
}

/** Reserved for platform adapters that expose a navigation pending signal. */
export function useIsNavigating(): boolean {
  return use(NavigationPendingContext);
}
