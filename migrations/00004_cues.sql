-- What the coach said during a stint, lap by lap, and its reading of each
-- driver afterwards.

-- One row per lap per job: the corner lines a client's report produced, or the
-- radio line a race lap produced. They are filed apart because they are written
-- by different things at different moments, and fetched together because a
-- client asks about a lap, not about a job.
CREATE TABLE lap_cues (
    id             bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    driver_slug    text        NOT NULL,
    -- The server's own identifier for the stint, which the client has from its
    -- upload and asks by. Not a foreign key, for the same reason driver_slug
    -- is not: this schema cannot reference core's tables.
    stint_id       text        NOT NULL,
    lap            integer     NOT NULL CHECK (lap > 0),
    -- cues, or racepace.
    job            text        NOT NULL,
    session        text        NOT NULL DEFAULT '',
    -- Which word limit applied: cue.training or cue.race.
    mode           text        NOT NULL DEFAULT '',
    -- What the corners were compared against, in the client's words.
    reference      text        NOT NULL DEFAULT '',
    -- The lines, as returned to the client. jsonb because nothing queries
    -- inside it: written once, read back whole.
    cues           jsonb       NOT NULL DEFAULT '[]'::jsonb,
    -- What this lap noted about the car, for the setup advice after the stint.
    setup_notes    jsonb       NOT NULL DEFAULT '[]'::jsonb,
    model          text        NOT NULL DEFAULT '',
    prompt_version text        NOT NULL DEFAULT '',
    written_at     timestamptz NOT NULL DEFAULT now(),
    -- A lap posted twice replaces what it said the first time.
    UNIQUE (stint_id, lap, job)
);

-- A driver's own page reads their latest, and the retention setting reads by age.
CREATE INDEX lap_cues_driver ON lap_cues (driver_slug, written_at DESC);
CREATE INDEX lap_cues_written ON lap_cues (written_at);

-- The debrief learns which stint it was about, so the client that uploaded the
-- stint can ask for it, and carries the setup changes when there was a sheet.
ALTER TABLE debriefs ADD COLUMN stint_id text NOT NULL DEFAULT '';
ALTER TABLE debriefs ADD COLUMN setup jsonb NOT NULL DEFAULT '[]'::jsonb;
CREATE INDEX debriefs_stint ON debriefs (driver_slug, stint_id);

-- The coach's reading of a driver: one row per driver, rewritten when an
-- administrator opens the page and a newer debrief exists than the one it
-- covers. through is that debrief's id.
CREATE TABLE profiles (
    driver_slug    text        PRIMARY KEY,
    driver_name    text        NOT NULL DEFAULT '',
    summary        text        NOT NULL,
    weaknesses     jsonb       NOT NULL DEFAULT '[]'::jsonb,
    through        bigint      NOT NULL DEFAULT 0,
    debriefs       integer     NOT NULL DEFAULT 0,
    model          text        NOT NULL DEFAULT '',
    prompt_version text        NOT NULL DEFAULT '',
    updated_at     timestamptz NOT NULL DEFAULT now()
);
