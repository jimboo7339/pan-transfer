// Package web 内嵌前端构建产物（web/dist）。构建前端后 go build 会自动带上。
package web

import "embed"

//go:embed all:dist
var DistFS embed.FS
