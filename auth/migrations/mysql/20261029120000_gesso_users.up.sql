CREATE TABLE gesso_users (
    id            BIGINT       AUTO_INCREMENT PRIMARY KEY,
    -- utf8mb4_bin: usernames match exactly, as on PostgreSQL and SQLite, not
    -- folded by MySQL's default case- and accent-insensitive collation.
    username      VARCHAR(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL UNIQUE,
    password_hash VARCHAR(255) NOT NULL,
    roles         TEXT         NOT NULL,
    created_at    BIGINT       NOT NULL
);
