CREATE INDEX CONCURRENTLY IF NOT EXISTS verification_slot_maintenance_name_created ON verification_slot_maintenance(name, created_at DESC);
