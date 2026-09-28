-- 当天流量明细：每个采集周期、每个 (设备, 目标) 组合一行，只追加。
-- MAC 与 IPv4 以整数存储以减小行与索引体积，可读形式见视图 v_usage_records。
CREATE TABLE usage_records (
    id           INTEGER PRIMARY KEY,         -- rowid 别名，无业务含义
    collected_at INTEGER NOT NULL,            -- 采集周期统一时间戳（Unix 秒）
    device_mac   INTEGER NOT NULL,            -- 设备稳定标识（48 位 MAC），无法换算时为 0
    device_ip    INTEGER NOT NULL,            -- 记录时刻的设备地址（IPv4，网络字节序），附加信息
    target_ip    INTEGER NOT NULL,            -- 目标地址（IPv4，网络字节序）
    domain       TEXT,                        -- DNS Map 查不到时为 NULL，允许事后补填
    bytes_up     INTEGER NOT NULL DEFAULT 0,  -- 设备→目标 字节增量
    bytes_down   INTEGER NOT NULL DEFAULT 0,  -- 目标→设备 字节增量
    CONSTRAINT uq_usage_records_collected_at_device_mac_target_ip
        UNIQUE (collected_at, device_mac, target_ip)
) STRICT;

CREATE INDEX idx_usage_records_device_mac_collected_at
    ON usage_records (device_mac, collected_at);

-- 供人工排查（sqlite3 命令行）使用的可读视图，程序本身不读取。
CREATE VIEW v_usage_records AS
SELECT
    id,
    collected_at,
    printf('%02x:%02x:%02x:%02x:%02x:%02x',
        (device_mac >> 40) & 255, (device_mac >> 32) & 255, (device_mac >> 24) & 255,
        (device_mac >> 16) & 255, (device_mac >> 8) & 255, device_mac & 255) AS device_mac,
    printf('%d.%d.%d.%d',
        (device_ip >> 24) & 255, (device_ip >> 16) & 255, (device_ip >> 8) & 255, device_ip & 255) AS device_ip,
    printf('%d.%d.%d.%d',
        (target_ip >> 24) & 255, (target_ip >> 16) & 255, (target_ip >> 8) & 255, target_ip & 255) AS target_ip,
    domain,
    bytes_up,
    bytes_down
FROM usage_records;

-- 库文件元数据，如 day_start（本文件所属统计日的起始时刻，Unix 秒）。
CREATE TABLE db_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
) STRICT, WITHOUT ROWID;
