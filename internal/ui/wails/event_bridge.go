package wailsui

import (
	"context"
	"time"

	"uniclog.io/sonoryx/internal/clientapp"
	"github.com/wailsapp/wails/v3/pkg/application"
)

func StartEventBridge(ctx context.Context, app *application.App, client *clientapp.App) func() {
	bridgeCtx, cancel := context.WithCancel(ctx)
	stateChanges, unsubscribeState := client.Subscribe(bridgeCtx)
	eventChanges, unsubscribeEvents := client.SubscribeEvents(bridgeCtx)
	go func() {
		for range stateChanges {
			app.Event.Emit("client-state-changed", true)
		}
	}()
	go func() {
		ticker := time.NewTicker(time.Second / 30)
		defer ticker.Stop()
		var lastSequence uint64
		for {
			select {
			case <-bridgeCtx.Done():
				return
			case <-ticker.C:
				sample := client.AudioMeterSnapshot()
				if sample.Sequence == lastSequence {
					continue
				}
				lastSequence = sample.Sequence
				app.Event.Emit("audio-meter", AudioMeterDTO{Input: sample.Input, Processed: sample.Processed, Transmitted: sample.Transmitted})
			}
		}
	}()
	go func() {
		for range eventChanges {
			app.Event.Emit("client-event-log-changed", true)
			app.Event.Emit("client-state-changed", true)
		}
	}()
	return func() {
		cancel()
		unsubscribeState()
		unsubscribeEvents()
	}
}
