//go:build production

package main

import (
	"embed"
	"io/fs"
)

//go:embed all:frontend/dist
var productionAssets embed.FS

var assets = mustProductionSub(productionAssets, "frontend/dist")

func mustProductionSub(root fs.FS, directory string) fs.FS {
	sub, err := fs.Sub(root, directory)
	if err != nil {
		panic(err)
	}
	return sub
}
