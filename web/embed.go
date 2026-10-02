package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var bundle embed.FS

// Assets 返回构建后内嵌的前端资源
func Assets() (fs.FS, error) {
	return fs.Sub(bundle, "dist")
}
