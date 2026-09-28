// Package nftctl 是 NFT Controller：通过 netlink 管理 LeoSentry 独立的
// nft 表 inet leosentry。表内有采集用的计数集合，以及按设备放行/拦截的集合。
// 管控只增删集合元素，不重建规则，也不修改 fw4 的表。
package nftctl
