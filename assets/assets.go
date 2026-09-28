// Package assets 内嵌随二进制发布的数据文件，如域名分类规则库和法定节假日。
package assets

import "embed"

// Rules 是 rules/ 目录下的全部规则文件（*.json）。
//
//go:embed rules
var Rules embed.FS

// Calendar 是 calendar/ 目录下的法定节假日 JSON。
//
//go:embed calendar
var Calendar embed.FS
