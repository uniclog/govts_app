package nativeaudio

import (
	"encoding/hex"
	"fmt"
	"uniclog.io/sonoryx/internal/audio"
	"unsafe"

	"github.com/gen2brain/malgo"
)

func ListDevices() (audio.DeviceList, error) {
	var list audio.DeviceList
	var err error
	withAudioThread(func() {
		list, err = listDevicesOnThread()
	})
	return list, err
}

func listDevicesOnThread() (audio.DeviceList, error) {
	ctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return audio.DeviceList{}, fmt.Errorf("initialize audio device context: %w", err)
	}
	defer func() {
		_ = ctx.Uninit()
		ctx.Free()
	}()

	capture, err := listDevices(ctx, malgo.Capture)
	if err != nil {
		return audio.DeviceList{}, fmt.Errorf("list capture devices: %w", err)
	}
	playback, err := listDevices(ctx, malgo.Playback)
	if err != nil {
		return audio.DeviceList{}, fmt.Errorf("list playback devices: %w", err)
	}
	return audio.DeviceList{Capture: capture, Playback: playback}, nil
}

func listDevices(ctx *malgo.AllocatedContext, kind malgo.DeviceType) ([]audio.DeviceInfo, error) {
	devices, err := ctx.Devices(kind)
	if err != nil {
		return nil, err
	}
	result := make([]audio.DeviceInfo, 0, len(devices))
	for _, device := range devices {
		result = append(result, audio.DeviceInfo{
			ID:        device.ID.String(),
			Name:      device.Name(),
			IsDefault: device.IsDefault != 0,
		})
	}
	return result, nil
}

func configureDeviceID(target *unsafe.Pointer, value string) (func(), error) {
	var id malgo.DeviceID
	if value == "" {
		return func() {}, nil
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) > len(id) {
		return nil, fmt.Errorf("invalid audio device ID %q", value)
	}
	copy(id[:], decoded)
	*target = id.Pointer()
	return func() {
		freeDeviceID(*target)
		*target = nil
	}, nil
}
