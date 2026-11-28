package filetransfer

import (
	"context"
	"net/http"

	"uniclog.io/sonoryx/apps/server/internal/filetransfer"
	"uniclog.io/sonoryx/apps/server/internal/persist"
	"uniclog.io/sonoryx/apps/server/internal/voice"
)

// New starts with real file permissions and storage in a temporary fixture directory.
func New(store *persist.Store, hub *voice.Hub, dir string) (interface {
	http.Handler
	Run(context.Context)
}, error) {
	cfg := filetransfer.DefaultConfig(dir)
	cfg.MinFreeBytes = 0
	return filetransfer.New(store, hub, voice.NewAdministration(store, hub), cfg)
}
