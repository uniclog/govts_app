//go:build !production

package main

import (
	"embed"
	"io/fs"
)

// The Wails development server proxies frontend requests. This fallback keeps
// ordinary Go builds and tests self-contained before npm has produced dist/.
//
//go:embed fallback/*
var developmentAssets embed.FS

var assets = mustSub(developmentAssets, "fallback")

func mustSub(root fs.FS, directory string) fs.FS {
	sub, err := fs.Sub(root, directory)
	if err != nil {
		panic(err)
	}
	return sub
}
