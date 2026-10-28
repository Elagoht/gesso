CREATE TABLE gesso_tokens (
    hash       CHAR(64)     PRIMARY KEY,
    subject    VARCHAR(255) NOT NULL,
    roles      TEXT         NOT NULL,
    created_at BIGINT       NOT NULL,
    expires_at BIGINT       NOT NULL
);
CREATE INDEX gesso_tokens_subject ON gesso_tokens (subject);
CREATE INDEX gesso_tokens_expires_at ON gesso_tokens (expires_at);
