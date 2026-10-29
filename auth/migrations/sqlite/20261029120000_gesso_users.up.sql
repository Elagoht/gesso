CREATE TABLE gesso_users (
    id            INTEGER PRIMARY KEY,
    username      TEXT    NOT NULL UNIQUE,
    password_hash TEXT    NOT NULL,
    roles         TEXT    NOT NULL,
    created_at    INTEGER NOT NULL
);
