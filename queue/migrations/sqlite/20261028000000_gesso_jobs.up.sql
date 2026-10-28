CREATE TABLE gesso_jobs (
    id           INTEGER PRIMARY KEY,
    kind         TEXT    NOT NULL,
    payload      BLOB    NOT NULL,
    status       TEXT    NOT NULL DEFAULT 'ready',
    attempts     INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL,
    run_at       INTEGER NOT NULL,
    locked_until INTEGER,
    last_error   TEXT    NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL
);
CREATE INDEX gesso_jobs_due ON gesso_jobs (kind, run_at) WHERE status IN ('ready', 'running');
