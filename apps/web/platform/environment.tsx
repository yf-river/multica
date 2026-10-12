"use client";
import { createContext, useContext } from "react";
export const EnvironmentContext = createContext("");
export const useEnvironmentId = () => useContext(EnvironmentContext);
