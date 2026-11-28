//go:build !windows || !(amd64 || arm64)

package nativeaudio

// StartLoopback needs WASAPI process loopback, which only Windows provides.
func StartLoopback(LoopbackTarget) (*LoopbackCapture, error) { return nil, ErrLoopbackUnsupported }

func LoopbackApps() ([]LoopbackApp, error) { return nil, ErrLoopbackUnsupported }
