CREATE UNIQUE INDEX CONCURRENTLY verification_slot_delivery ON verification_slot(delivery_id) WHERE delivery_id IS NOT NULL;
