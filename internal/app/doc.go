// Package app 负责进程生命周期：加载配置、组装各模块依赖、
// 启动/停止 Collector → Analyzer → Policy → NFT Controller 流水线与 HTTP 服务，
// 处理信号与优雅退出。
package app
