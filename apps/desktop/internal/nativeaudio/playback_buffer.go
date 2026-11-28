package nativeaudio

import "sync"

type playbackBuffer struct {
	mu    sync.Mutex
	data  []byte
	limit int
}

func (b *playbackBuffer) Read(dst []byte) (int, error) {
	b.read(dst)
	return len(dst), nil
}

func (b *playbackBuffer) read(dst []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := copy(dst, b.data)
	clear(dst[n:])
	b.data = b.data[n:]
}

// push reports how many queued or new bytes it discarded to stay in limit.
func (b *playbackBuffer) push(data []byte) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(data) >= b.limit {
		dropped := len(b.data) + len(data) - b.limit
		b.data = append(b.data[:0], data[len(data)-b.limit:]...)
		return dropped
	}
	excess := len(b.data) + len(data) - b.limit
	if excess > 0 {
		b.data = b.data[excess:]
	} else {
		excess = 0
	}
	b.data = append(b.data, data...)
	return excess
}

func (b *playbackBuffer) len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.data)
}

func (b *playbackBuffer) clear() {
	b.mu.Lock()
	b.data = nil
	b.mu.Unlock()
}
