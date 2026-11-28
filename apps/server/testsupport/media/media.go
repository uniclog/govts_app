// Package media exposes the Sonoryx media fixture to web integration tests.
package media

import (
	"net/http"

	"uniclog.io/sonoryx/apps/server/internal/media"
	"uniclog.io/sonoryx/apps/server/internal/voice"
)

func New(hub *voice.Hub) (http.Handler, func(), error) {
	manager, err := media.NewManager(hub)
	if err != nil {
		return nil, nil, err
	}
	handler, err := media.NewHTTPHandler(hub, manager)
	if err != nil {
		manager.Close()
		return nil, nil, err
	}
	return handler, manager.Close, nil
}
