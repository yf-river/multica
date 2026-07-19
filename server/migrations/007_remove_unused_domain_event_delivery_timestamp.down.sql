ALTER TABLE domain_event_delivery
    ADD COLUMN delivered_at timestamp with time zone DEFAULT now() NOT NULL;
