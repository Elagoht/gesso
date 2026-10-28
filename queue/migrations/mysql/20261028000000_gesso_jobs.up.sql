CREATE TABLE gesso_jobs (
    id           BIGINT       AUTO_INCREMENT PRIMARY KEY,
    kind         VARCHAR(255) NOT NULL,
    payload      LONGBLOB     NOT NULL,
    status       VARCHAR(16)  NOT NULL DEFAULT 'ready',
    attempts     INTEGER      NOT NULL DEFAULT 0,
    max_attempts INTEGER      NOT NULL,
    run_at       BIGINT       NOT NULL,
    locked_until BIGINT,
    last_error   TEXT         NOT NULL,
    created_at   BIGINT       NOT NULL,
    INDEX gesso_jobs_due (kind, status, run_at)
);
