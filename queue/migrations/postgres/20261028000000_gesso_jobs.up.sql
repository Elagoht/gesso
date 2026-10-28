CREATE TABLE gesso_jobs (
    id           BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind         TEXT        NOT NULL,
    payload      BYTEA       NOT NULL,
    status       TEXT        NOT NULL DEFAULT 'ready',
    attempts     INTEGER     NOT NULL DEFAULT 0,
    max_attempts INTEGER     NOT NULL,
    run_at       TIMESTAMPTZ NOT NULL,
    locked_until TIMESTAMPTZ,
    last_error   TEXT        NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL
);
CREATE INDEX gesso_jobs_due ON gesso_jobs (kind, run_at) WHERE status IN ('ready', 'running');
