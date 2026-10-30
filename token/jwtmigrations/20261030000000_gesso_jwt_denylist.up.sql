CREATE TABLE gesso_jwt_denylist (
    entry          VARCHAR(255) PRIMARY KEY,
    revoked_before BIGINT       NOT NULL,
    expires_at     BIGINT       NOT NULL
);
CREATE INDEX gesso_jwt_denylist_expires_at ON gesso_jwt_denylist (expires_at);
