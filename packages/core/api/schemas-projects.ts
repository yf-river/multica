import { z } from "zod";
import type {
  ListProjectResourcesResponse,
  ListProjectsResponse,
  Project,
  ProjectResource,
  SearchProjectsResponse,
} from "../types";
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

export const ProjectSchema = z.object({
  id: NonEmptyStringSchema,
  workspace_id: NonEmptyStringSchema,
  title: z.string(),
  description: z.string().nullable(),
  icon: z.string().nullable(),
  status: z.string(),
  priority: z.string(),
  lead_type: z.string().nullable(),
  lead_id: z.string().nullable(),
  created_at: z.string(),
  updated_at: z.string(),
  issue_count: z.number().default(0),
  done_count: z.number().default(0),
  resource_count: z.number().default(0),
}).loose();

export const ListProjectsResponseSchema = z.object({
  projects: z.array(ProjectSchema).default([]),
  total: z.number().default(0),
}).loose();

export const SearchProjectSchema = ProjectSchema.extend({
  match_source: z.string(),
  matched_snippet: z.string().optional(),
}).loose();

export const SearchProjectsResponseSchema = z.object({
  projects: z.array(SearchProjectSchema).default([]),
  total: z.number().default(0),
}).loose();

export const EMPTY_PROJECT: Project = {
  id: "",
  workspace_id: "",
  title: "",
  description: null,
  icon: null,
  status: "planned",
  priority: "none",
  lead_type: null,
  lead_id: null,
  created_at: "",
  updated_at: "",
  issue_count: 0,
  done_count: 0,
  resource_count: 0,
};

export const EMPTY_PROJECT_LIST_RESPONSE: ListProjectsResponse = {
  projects: [],
  total: 0,
};

export const EMPTY_SEARCH_PROJECTS_RESPONSE: SearchProjectsResponse = {
  projects: [],
  total: 0,
};
