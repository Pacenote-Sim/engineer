-- Every editable piece of the prompt, not just the persona.
--
-- 00002 kept one row because one thing was editable. What an operator actually
-- wants to change is any of it: how a cue is asked for, what differs in a race,
-- how the debrief reads, how setup advice is worded. So the table is keyed by
-- which part it is, and a part nobody has written stays in the binary.
--
-- What is not here is the tool schemas. They are the shape an answer comes back
-- in rather than a request about its tone, and editing one does not change what
-- the coach says — it stops the answer being readable.
CREATE TABLE prompt_parts (
    -- Which piece. The binary knows the list; the database does not need to,
    -- and a part the binary no longer has is ignored rather than breaking a
    -- downgrade.
    part       text        PRIMARY KEY,
    markdown   text        NOT NULL CHECK (length(btrim(markdown)) > 0),
    -- Every answer is stamped with a digest over the parts that produced it, so
    -- a change in what the coach says traces to the edit that caused it.
    sha256     text        NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    updated_by text        NOT NULL DEFAULT ''
);

-- Anything already uploaded was the persona, so it becomes the persona.
INSERT INTO prompt_parts (part, markdown, sha256, updated_at, updated_by)
SELECT 'persona', markdown, sha256, uploaded_at, uploaded_by FROM agent;

DROP TABLE agent;
