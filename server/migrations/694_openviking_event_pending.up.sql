CREATE INDEX CONCURRENTLY openviking_event_pending ON openviking_event (created_at, id) WHERE delivered_at IS NULL;
