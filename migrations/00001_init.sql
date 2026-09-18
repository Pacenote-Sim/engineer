-- The engineer's own tables, in the engineer's own schema.
--
-- The host creates the schema and runs this as a role that owns it and nothing
-- else, so everything here is unqualified and lands where it should. There is no
-- down migration and there is not meant to be: removing this plugin drops the
-- schema whole, which is the only rollback that is ever actually correct.

-- One debrief per session, as written.
--
-- driver_slug is the server's own identifier for the driver and is what the
-- core_read views join on. It is deliberately not a foreign key: this schema
-- cannot reference core's tables, and it should not — a driver removed from the
-- server should not silently delete the coaching they were given, and an
-- operator who wants that has the retention setting.
CREATE TABLE debriefs (
    id           bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    driver_slug  text        NOT NULL,
    -- The driver's name as it was at the time. The current one comes from the
    -- core_read view on read; this is the fallback for a driver core no longer
    -- has, so an old debrief still says whose it was.
    driver_name  text        NOT NULL DEFAULT '',
    sim          text        NOT NULL DEFAULT '',
    track        text        NOT NULL DEFAULT '',
    car          text        NOT NULL DEFAULT '',
    -- What the driver reads.
    summary      text        NOT NULL,
    -- The findings, as the model returned them. jsonb because nothing queries
    -- inside it: it is written once with the debrief, read back whole, and
    -- never filtered or aggregated on. A findings table would buy an index
    -- nobody reads and a second write on a path that already has one.
    findings     jsonb       NOT NULL DEFAULT '[]'::jsonb,
    -- What produced it. Stored because the only way to tell whether a change to
    -- the prompt or the model made the coach better is to read what each wrote.
    model          text      NOT NULL DEFAULT '',
    prompt_version text      NOT NULL DEFAULT '',
    written_at   timestamptz NOT NULL DEFAULT now()
);

-- The one query this table has: a driver's last few, newest first.
CREATE INDEX debriefs_driver ON debriefs (driver_slug, written_at DESC);

-- And the one the retention setting runs.
CREATE INDEX debriefs_written ON debriefs (written_at);
