-- 设备身份库：不随 today.db 每日切换，保存在闪存 DataDir。
-- MAC / IPv4 与 usage_records 相同，以整数存储。
CREATE TABLE devices (
    mac        INTEGER PRIMARY KEY,           -- 48 位 MAC，设备稳定标识
    name       TEXT    NOT NULL DEFAULT '',   -- 用户起的名字，空串表示未命名
    first_seen INTEGER NOT NULL,              -- 首次出现（Unix 秒）
    last_seen  INTEGER NOT NULL,              -- 最近一次观察到（Unix 秒）
    last_ip    INTEGER NOT NULL DEFAULT 0     -- 最近一次 IPv4，0 表示尚无
) STRICT;

CREATE TABLE device_ips (
    mac        INTEGER NOT NULL,
    ip         INTEGER NOT NULL,              -- 历史上用过的 IPv4
    first_seen INTEGER NOT NULL,
    last_seen  INTEGER NOT NULL,
    PRIMARY KEY (mac, ip)
) STRICT, WITHOUT ROWID;
