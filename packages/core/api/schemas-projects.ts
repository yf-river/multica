import { z } from "zod";
import type { ListProjectResourcesResponse, ProjectResource } from "../types";
import { NonEmptyStringSchema } from "./schemas-internal";

export const ProjectResourceSchema = z.object({
  id: NonEmptyStringSchema,
  project_id: NonEmptyStringSchema,
  workspace_id: NonEmptyStringSchema,
  resource_type: NonEmptyStringSchema,
  resource_ref: z.record(z.string(), z.unknown()),
  label: z.string().nullable().optional().transform((value) => value ?? null),
  position: z.number(),
  created_at: z.string().default(""),
  created_by: z.string().nullable().optional().transform((value) => value ?? null),
}).loose();

export const ProjectResourceListResponseSchema = z.object({
  resources: z.array(ProjectResourceSchema).default([]),
  total: z.number().default(0),
}).loose();

export const EMPTY_PROJECT_RESOURCE: ProjectResource = {
  id: "",
  project_id: "",
  workspace_id: "",
  resource_type: "",
  resource_ref: {},
  label: null,
  position: 0,
  created_at: "",
  created_by: null,
};

export const EMPTY_PROJECT_RESOURCE_LIST_RESPONSE: ListProjectResourcesResponse = {
  resources: [],
  total: 0,
};
