// Package version embeds the release configuration shared by all binaries.
package version

import (
	_ "embed"
	"encoding/json"
)

//go:embed version.json
var releaseJSON []byte

type Release struct {
	Apps             AppVersions `json:"apps"`
	MinServerVersion string      `json:"minServerVersion"`
}

type AppVersions struct {
	Desktop    string `json:"desktop"`
	Server     string `json:"server"`
	WebGateway string `json:"webgateway"`
	Web        string `json:"web"`
}

func Read() (Release, error) {
	var release Release
	err := json.Unmarshal(releaseJSON, &release)
	return release, err
}
