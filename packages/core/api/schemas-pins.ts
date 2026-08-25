import { z } from "zod";
import type { PinnedItem } from "../types";
import { NonEmptyStringSchema } from "./schemas-internal";

export const PinnedItemSchema = z.object({
  id: NonEmptyStringSchema,
  item_type: z.string(),
  item_id: NonEmptyStringSchema,
}).loose();

export const PinnedItemListSchema = z.array(PinnedItemSchema);
export const EMPTY_PINNED_ITEM_LIST: PinnedItem[] = [];
