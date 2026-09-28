# LeoSentry

运行在软路由上的家庭上网行为监测与管控服务。识别家庭网络中各设备的上网行为（尤其是游戏、视频等），
支持在 Web 页面上手动管控，或按可编辑的策略/规则自动管控（如限制孩子的上网时段与游戏时长）。

## 目标平台

| 项目 | 说明 |
| --- | --- |
| 系统 | ImmortalWrt 25.12.2 |
| 硬件 | NanoPi R2S / RK3328 / ARM64 |
| 防火墙 | firewall4 / nftables |
| 语言 | Go（`CGO_ENABLED=0` 静态交叉编译） |
| 存储 | SQLite（纯 Go 驱动） |
| 接口 | REST API + 静态 HTML/CSS/JS Web UI（embed 进二进制） |

## 设计原则

**LeoSentry 不处理任何网络包。** 所有转发、过滤仍由 Linux kernel + nftables 完成，程序只做三件事：

> **观察 → 判断 → 改规则**

- **观察**：周期性读取内核状态（conntrack、nft 计数器）和 DNS 解析结果，不抓包、不走用户态转发。
- **判断**：在内存中关联、聚合出设备行为，交给策略引擎评估。
- **改规则**：只维护独立的 nft 表 `inet leosentry`，通过 set/map 元素做差量更新，不整表重建，也不改动 fw4 的表。

## 架构

```
Browser ──HTTP──▶ Static Web + REST API
                        │
                        ▼
  Collector ──▶ Analyzer ──▶ Policy Engine ──▶ NFT Controller ──▶ nftables / fw4 ──▶ Internet
  (DNS Map      (Activity     (Schedule          (inet leosentry
   Conntrack     Category      Quota              差量更新)
   NFT stats)    Game)         Block/Allow)
```

### 数据流

1. **DNS Map**：跟踪 dnsmasq 查询日志（启动时读取已有日志预热），维护 `IP → 域名`，30 分钟未刷新即过期。
2. **周期采集**：每 60 秒读取 `inet leosentry` 表中按"设备IP . 目标IP"计数的 nft 集合，差分得到上下行增量，过滤掉每分钟不足 8 KB 的心跳类小流量，结合 IP→MAC 对照表与 DNS Map 组装成记录，写入闪存上的 `today.db`（每天约数 MB，剩余空间不足时自动暂停写入）与内存当日计数器；conntrack 快照差分每 10 秒一次，只用于实时在线状态。

采集层（第 1、2 步）已实现，详细设计见 [docs/采集层技术设计.md](docs/采集层技术设计.md)。
3. **分析**：服务端扫一遍当天明细、用规则库分类，聚合成「设备 × 10 分钟 × 应用」快照；浏览器负责筛选、排序和图表。Web 页见 [docs/Web展示技术设计.md](docs/Web展示技术设计.md)。
4. **决策**：策略引擎根据时间段、配额和规则计算每台设备的期望控制状态（未实现）。
5. **执行**：NFT Controller 对比期望状态和当前状态，只下发差量（未实现）。

## 目录结构

```
.
├── cmd/leosentry/            # 程序入口：运行服务 / install / uninstall / version
├── internal/
│   ├── app/                  # 生命周期、依赖组装、流水线调度
│   ├── config/               # UCI 配置加载与校验
│   ├── sysconf/              # 启动时自动完成系统设置（dnsmasq 日志、流量卸载、时区）
│   ├── installer/            # 安装为 procd 服务
│   ├── model/                # 共享领域模型
│   ├── device/               # 设备识别：DHCP 租约、静态绑定、邻居表
│   ├── collector/            # 采集主循环
│   │   ├── dns/              #   DNS Map（dnsmasq 查询日志）
│   │   ├── conntrack/        #   conntrack 快照差分（实时在线状态）
│   │   └── nftstats/         #   nft 流量计数读取与差分
│   ├── usage/                # 内存当日累计计数器
│   ├── statday/              # 统计日边界（默认 03:00 切换）
│   ├── uci/                  # UCI 文件解析与 uci 命令封装
│   ├── fswatch/              # inotify 文件变更通知
│   ├── analyzer/             # 行为分析
│   │   ├── activity/         #   时长/流量聚合
│   │   ├── category/         #   域名分类
│   │   └── game/             #   游戏识别
│   ├── policy/               # 策略引擎
│   │   ├── rule/             #   规则定义与求值
│   │   ├── schedule/         #   时间段策略
│   │   └── quota/            #   配额策略
│   ├── nftctl/               # NFT Controller（独立表 inet leosentry）
│   ├── store/                # SQLite：today.db 初始化、写入、每日切换与归档
│   │   └── migrations/       #   内嵌的版本化迁移脚本
│   └── api/                  # REST API
│       ├── handler/          #   资源处理器
│       └── middleware/       #   认证、日志、恢复
├── web/                      # go:embed 静态资源包
│   └── static/               #   HTML / CSS / JS / 图片
├── api/                      # OpenAPI 接口文档
├── assets/rules/             # 内置规则库
│   ├── categories/           #   域名分类列表
│   └── games/                #   游戏特征（域名/端口）
├── configs/                  # 示例配置
├── deploy/openwrt/           # OpenWrt 软件包打包（预留）
├── docs/                     # 设计文档
├── scripts/                  # 开发/部署辅助脚本
├── test/                     # 集成测试
├── Makefile
└── go.mod
```

依赖方向：`api → policy/analyzer/store`，`app` 负责组装所有模块；`model` 不依赖任何 internal 包；
`collector`、`nftctl` 只和内核交互，不依赖 `api`。

## 性能要点（R2S 资源有限）

- 使用 netlink 直连内核，不 fork `conntrack` / `nft` 命令行。
- 采集结果只在内存中聚合，按批写入 SQLite（WAL 模式），减少对存储卡的写入。
- DNS Map、设备表常驻内存，采用适合高频读取的结构，按 TTL 淘汰。
- nft 规则用 set/map 表达，更新只改元素，内核侧匹配复杂度与规则数量无关。
- 纯 Go 静态编译，单二进制部署，无 CGO 依赖。

## 构建

```sh
make build        # 本机构建
make build-arm64  # 交叉编译到 R2S（linux/arm64）
make test
```

## 部署

```sh
scp bin/leosentry-linux-arm64 root@192.168.1.1:/tmp/leosentry
ssh root@192.168.1.1 'chmod +x /tmp/leosentry && /tmp/leosentry install'
```

`install` 会安装二进制、procd 启动脚本和默认配置 `/etc/config/leosentry`，并设置开机自启。
程序启动时自动开启 dnsmasq 查询日志（会注释掉 `/etc/dnsmasq.conf` 中冲突的 `log-facility` 行，重启后检查 dnsmasq 是否正常，异常则自动回滚）、关闭 fw4 流量卸载、创建 nft 表，无需手工执行命令。
运行日志通过 `logread -e leosentry` 查看。

局域网浏览器打开 `http://<软路由 LAN IP>:8088/` 即可看今日概览（默认只监听 LAN 地址）。
`http_port` 为 `0` 时关闭 Web；`http_address` 可指定监听 IP。页面目前没有登录，上管控功能前需要补认证。
