package nativeaudio

import (
	"bytes"
	"sync"
	"testing"
)

func TestPlaybackBufferBoundedAndClear(t *testing.T) {
	b := &playbackBuffer{limit: 4}
	b.push([]byte{1, 2, 3, 4})
	b.push([]byte{5, 6})
	out := make([]byte, 6)
	n, err := b.Read(out)
	if err != nil || n != 6 || !bytes.Equal(out, []byte{3, 4, 5, 6, 0, 0}) {
		t.Fatalf("read=%v %d %v", out, n, err)
	}
	b.push([]byte{1, 2, 3, 4, 5, 6})
	b.clear()
	b.Read(out)
	if !bytes.Equal(out, make([]byte, 6)) {
		t.Fatal("clear left old playback")
	}
}

func TestPlaybackBufferConcurrentReadWriteClear(t *testing.T) {
	b := &playbackBuffer{limit: 1920}
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				switch i {
				case 0:
					b.push(make([]byte, 960))
				case 1:
					b.Read(make([]byte, 512))
				case 2:
					b.clear()
				}
			}
		}(i)
	}
	wg.Wait()
}
