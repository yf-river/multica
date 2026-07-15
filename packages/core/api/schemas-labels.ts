import { z } from "zod";
import type { Label } from "../types";
import { NonEmptyStringSchema } from "./schemas-internal";

export const LabelSchema = z.object({
  id: NonEmptyStringSchema,
  name: z.string(),
  color: z.string().regex(/^#[0-9a-f]{6}$/),
}).loose();

export const LabelListSchema = z.object({
  labels: z.array(LabelSchema).default([]),
}).loose().transform(({ labels }) => labels);

export const IssueLabelListSchema = z.object({
  labels: z.array(LabelSchema),
}).loose().transform(({ labels }) => labels);

export const EMPTY_LABELS: Label[] = [];
