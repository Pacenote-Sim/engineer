-- The persona, when an operator has replaced it.
--
-- What the coach sounds like is the operator's — a GT endurance team and a rally
-- team want different words, and so do two GT teams. What the coach is allowed
-- to say is not: the cue rules and the tool schemas live in the binary next to
-- the validator that enforces them, because a prompt an operator can edit into
-- disagreeing with the validator is a stream of discarded cues nobody can
-- explain.
--
-- It lives here rather than beside the binary because the plugin's directory is
-- what gets replaced on an upgrade. A file there is a file the next version
-- overwrites; a row here is one an operator wrote once and keeps.
CREATE TABLE agent (
    -- One row, enforced. There is one persona at a time, and a table that can
    -- hold two is a table that will.
    id          boolean     PRIMARY KEY DEFAULT true CHECK (id),
    -- The markdown as uploaded, kept exactly. It is what the operator reads
    -- back and edits, so reformatting it would be losing their work.
    markdown    text        NOT NULL CHECK (length(btrim(markdown)) > 0),
    -- The digest is stamped on every answer beside the prompt version, so a
    -- change in what the coach says can be traced to the file that changed it.
    sha256      text        NOT NULL,
    uploaded_at timestamptz NOT NULL DEFAULT now(),
    -- The administrator the host says uploaded it. It is the host's word, taken
    -- from its own session, and it is here so an operator can ask who changed
    -- the coach.
    uploaded_by text        NOT NULL DEFAULT ''
);
