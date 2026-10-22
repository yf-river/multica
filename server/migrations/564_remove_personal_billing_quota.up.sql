-- Personal workbench no longer uses commercial billing or platform execution quotas.
DROP TABLE IF EXISTS seat_capacity_outbox;
DROP TABLE IF EXISTS autopilot_quota_reservation;
DROP TABLE IF EXISTS autopilot_quota_period;
ALTER TABLE autopilot_run DROP COLUMN IF EXISTS quota_reservation_id;
