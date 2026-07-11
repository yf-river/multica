import { z } from "zod";
import type { IssueLabelsResponse, Label, ListLabelsResponse } from "../types";
import { NonEmptyStringSchema } from "./schemas-internal";

export const LabelSchema = z.object({
  id: NonEmptyStringSchema,
  workspace_id: NonEmptyStringSchema,
  name: z.string(),
  color: z.string().regex(/^#[0-9a-f]{6}$/),
  created_at: z.string(),
  updated_at: z.string(),
}).loose();

export const ListLabelsResponseSchema = z.object({
  labels: z.array(LabelSchema).default([]),
  total: z.number().default(0),
}).loose();

export const IssueLabelsResponseSchema = z.object({
  labels: z.array(LabelSchema).default([]),
}).loose();

export const EMPTY_LABEL: Label = {
  id: "",
  workspace_id: "",
  name: "",
  color: "#000000",
  created_at: "",
  updated_at: "",
};

export const EMPTY_LABEL_LIST_RESPONSE: ListLabelsResponse = { labels: [], total: 0 };
export const EMPTY_ISSUE_LABELS_RESPONSE: IssueLabelsResponse = { labels: [] };
