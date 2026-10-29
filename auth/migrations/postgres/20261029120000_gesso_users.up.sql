CREATE TABLE gesso_users (
    id            BIGINT       GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    username      VARCHAR(255) NOT NULL UNIQUE,
    password_hash VARCHAR(255) NOT NULL,
    roles         TEXT         NOT NULL,
    created_at    BIGINT       NOT NULL
);
