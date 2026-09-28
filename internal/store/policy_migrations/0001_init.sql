-- 管控策略库：不随 today.db 每日切换。日历按公历日存一整年，策略按 MAC 存。
CREATE TABLE calendar_year (
    year        INTEGER PRIMARY KEY,
    region      TEXT    NOT NULL,
    source      TEXT    NOT NULL,
    document    TEXT    NOT NULL,
    published   TEXT    NOT NULL,
    source_url  TEXT    NOT NULL,
    imported_at INTEGER NOT NULL
) STRICT;

CREATE TABLE calendar_day (
    date     TEXT    PRIMARY KEY,
    year     INTEGER NOT NULL,
    weekday  INTEGER NOT NULL,
    workday  INTEGER NOT NULL,
    name     TEXT    NOT NULL DEFAULT '',
    kind     TEXT    NOT NULL DEFAULT ''
) STRICT;

CREATE TABLE vacation (
    kind       TEXT PRIMARY KEY,
    start_date TEXT NOT NULL,
    end_date   TEXT NOT NULL
) STRICT;

CREATE TABLE device_policy (
    mac          TEXT    PRIMARY KEY,
    enabled      INTEGER NOT NULL CHECK (enabled IN (0, 1)),
    paused       INTEGER NOT NULL CHECK (paused IN (0, 1)),
    extend_until INTEGER NOT NULL,
    spec         TEXT    NOT NULL,
    updated_at   INTEGER NOT NULL
) STRICT;
