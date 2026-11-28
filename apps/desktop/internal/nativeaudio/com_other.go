//go:build !windows

package nativeaudio

func withAudioThread(fn func()) { fn() }
