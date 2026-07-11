import { z } from "zod";
import type { PinnedItem } from "../types";
import { NonEmptyStringSchema } from "./schemas-internal";

export const PinnedItemSchema = z.object({
  id: NonEmptyStringSchema,
  workspace_id: NonEmptyStringSchema,
  user_id: NonEmptyStringSchema,
  item_type: z.string(),
  item_id: NonEmptyStringSchema,
  position: z.number(),
  created_at: z.string(),
}).loose();

export const PinnedItemListSchema = z.array(PinnedItemSchema);
export const EMPTY_PINNED_ITEM_LIST: PinnedItem[] = [];
export const EMPTY_PINNED_ITEM: PinnedItem = {
  id: "",
  workspace_id: "",
  user_id: "",
  item_type: "issue",
  item_id: "",
  position: 0,
  created_at: "",
};
