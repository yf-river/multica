import { z } from "zod";
import type {
  Project,
  ProjectResource,
  SearchProjectResult,
} from "../types";
import { NonEmptyStringSchema } from "./schemas-internal";

export const ProjectResourceSchema = z.object({
  id: NonEmptyStringSchema,
  project_id: NonEmptyStringSchema,
  resource_type: NonEmptyStringSchema,
  resource_ref: z.record(z.string(), z.unknown()),
  label: z.string().nullable().optional().transform((value) => value ?? null),
}).loose();

export const ProjectResourceListSchema = z.object({
  resources: z.array(ProjectResourceSchema).default([]),
}).loose().transform(({ resources }) => resources);

export const EMPTY_PROJECT_RESOURCE: ProjectResource = {
  id: "",
  project_id: "",
  resource_type: "",
  resource_ref: {},
  label: null,
};

export const EMPTY_PROJECT_RESOURCES: ProjectResource[] = [];

export const ProjectSchema = z.object({
  id: NonEmptyStringSchema,
  title: z.string(),
  description: z.string().nullable(),
  icon: z.string().nullable(),
  status: z.string(),
  priority: z.string(),
  lead_type: z.string().nullable(),
  lead_id: z.string().nullable(),
  created_at: z.string(),
  issue_count: z.number().default(0),
  done_count: z.number().default(0),
  resource_count: z.number().default(0),
}).loose();

export const ProjectListSchema = z.object({
  projects: z.array(ProjectSchema).default([]),
}).loose().transform(({ projects }) => projects);

const SearchProjectSchema = ProjectSchema.extend({
  match_source: z.string(),
  matched_snippet: z.string().optional(),
}).loose();

export const SearchProjectListSchema = z.object({
  projects: z.array(SearchProjectSchema).default([]),
}).loose().transform(({ projects }) => projects);

export const EMPTY_PROJECT: Project = {
  id: "",
  title: "",
  description: null,
  icon: null,
  status: "planned",
  priority: "none",
  lead_type: null,
  lead_id: null,
  created_at: "",
  issue_count: 0,
  done_count: 0,
  resource_count: 0,
};

export const EMPTY_PROJECTS: Project[] = [];
export const EMPTY_SEARCH_PROJECTS: SearchProjectResult[] = [];
