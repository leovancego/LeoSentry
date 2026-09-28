// Package store 是 SQLite 持久化层（纯 Go 驱动 modernc.org/sqlite，CGO_ENABLED=0 交叉编译）。
// 闪存上只保留当天的 today.db，表结构在程序启动时由 Open 初始化，
// 通过 PRAGMA user_version 版本化，每个库文件的每个迁移只执行一次。
// 每日切换时把 today.db 移入待归档目录并新建空库，后台复制到外部硬盘并校验。
// 设备身份（别名、首次出现、历史 IP）存在同目录的 devices.db，不随统计日切换。
package store
