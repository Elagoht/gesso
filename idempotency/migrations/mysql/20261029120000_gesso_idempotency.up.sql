CREATE TABLE gesso_idempotency (
    id           VARCHAR(64) PRIMARY KEY,
    fingerprint  VARCHAR(64) NOT NULL,
    state        VARCHAR(16) NOT NULL,
    status       INTEGER     NOT NULL,
    content_type VARCHAR(255) NOT NULL,
    location     VARCHAR(2048) NOT NULL,
    body         LONGBLOB       NOT NULL,
    locked_until BIGINT      NOT NULL,
    expires_at   BIGINT      NOT NULL
);
CREATE INDEX gesso_idempotency_expires ON gesso_idempotency (expires_at);
