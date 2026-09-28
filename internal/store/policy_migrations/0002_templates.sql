CREATE TABLE policy_template (
    id         TEXT    PRIMARY KEY,
    name       TEXT    NOT NULL,
    spec       TEXT    NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

CREATE TABLE app_setting (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) STRICT;
