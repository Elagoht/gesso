CREATE TABLE gesso_ratelimit (
    client VARCHAR(255) PRIMARY KEY,
    tat    BIGINT       NOT NULL
);
CREATE INDEX gesso_ratelimit_tat ON gesso_ratelimit (tat);
