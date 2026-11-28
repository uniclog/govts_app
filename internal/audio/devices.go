package audio

type DeviceInfo struct {
	ID        string
	Name      string
	IsDefault bool
}

type DeviceList struct {
	Capture  []DeviceInfo
	Playback []DeviceInfo
}
