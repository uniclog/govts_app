//go:build !linux

package media

const clockTicks = 100

// Process CPU/RSS and socket counters are read only on Linux servers.
func readHostSample(*bufferedMediaNet) hostSample { return hostSample{} }
