package client

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"uniclog.io/sonoryx/internal/audio"
)

type decoderFunc func([]byte) ([]int16, error)

func TestDecodeLoopDropsBadFrameAndResetsOnlyItsSender(t *testing.T) {
	input := make(chan audio.MediaFrame, 5)
	for _, frame := range []audio.MediaFrame{
		{SenderID: 1, Sequence: 1},
		{SenderID: 2, Sequence: 1},
		{SenderID: 1, Sequence: 2, Data: []byte{3}},
		{SenderID: 2, Sequence: 2},
		{SenderID: 1, Sequence: 3},
	} {
		input <- frame
	}
	close(input)
	output := make(chan audio.MediaPCMFrame, 5)
	created := int16(0)
	err := DecodeLoop(context.Background(), func() (audio.Decoder, error) {
		created++
		id, calls := created, int16(0)
		return decoderFunc(func(data []byte) ([]int16, error) {
			calls++
			if len(data) != 0 {
				return nil, errors.New("bad frame")
			}
			return []int16{id, calls}, nil
		}), nil
	}, input, output)
	if err != nil {
		t.Fatal(err)
	}
	var got []audio.MediaPCMFrame
	for frame := range output {
		got = append(got, frame)
	}
	want := []audio.MediaPCMFrame{
		{SenderID: 1, Sequence: 1, Samples: []int16{1, 1}},
		{SenderID: 2, Sequence: 1, Samples: []int16{2, 1}},
		{SenderID: 2, Sequence: 2, Samples: []int16{2, 2}},
		{SenderID: 1, Sequence: 3, Samples: []int16{3, 1}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded frames = %+v, want %+v", got, want)
	}
}

func TestDecodeLoopSurvivesMalformedOpus(t *testing.T) {
	config := audio.CodecConfig{SampleRate: 48000, Channels: 1, SamplesPerFrame: 960}
	encoder, err := audio.NewOpusEncoder(config)
	if err != nil {
		t.Fatal(err)
	}
	valid, err := encoder.Encode(make([]int16, 960))
	if err != nil {
		t.Fatal(err)
	}
	input := make(chan audio.MediaFrame, 2)
	input <- audio.MediaFrame{SenderID: 1, Sequence: 1, Data: []byte{3}}
	input <- audio.MediaFrame{SenderID: 1, Sequence: 2, Data: valid}
	close(input)
	output := make(chan audio.MediaPCMFrame, 2)
	if err := DecodeLoop(context.Background(), func() (audio.Decoder, error) {
		return audio.NewOpusDecoder(config)
	}, input, output); err != nil {
		t.Fatal(err)
	}
	frame, ok := <-output
	if !ok || frame.Sequence != 2 || len(frame.Samples) != 960 {
		t.Fatalf("valid frame was not decoded: %+v", frame)
	}
	if _, ok := <-output; ok {
		t.Fatal("unexpected extra frame")
	}
}

func TestDecodeLoopPropagatesInitializationFailure(t *testing.T) {
	input := make(chan audio.MediaFrame, 1)
	input <- audio.MediaFrame{SenderID: 1}
	close(input)
	want := errors.New("initialization failed")
	err := DecodeLoop(context.Background(), func() (audio.Decoder, error) {
		return nil, want
	}, input, make(chan audio.MediaPCMFrame, 1))
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func (f decoderFunc) Decode(data []byte) ([]int16, error) {
	return f(data)
}

func TestDecodeLoopKeepsDecoderStatePerSender(t *testing.T) {
	encodedCh := make(chan audio.MediaFrame, 3)
	pcmOutCh := make(chan audio.MediaPCMFrame, 3)

	encodedCh <- audio.MediaFrame{SenderID: 10, Sequence: 1}
	encodedCh <- audio.MediaFrame{SenderID: 20, Sequence: 7}
	encodedCh <- audio.MediaFrame{SenderID: 10, Sequence: 2}
	close(encodedCh)

	created := 0
	newDecoder := func() (audio.Decoder, error) {
		created++
		decoderID := int16(created)
		calls := int16(0)

		return decoderFunc(func([]byte) ([]int16, error) {
			calls++
			return []int16{decoderID, calls}, nil
		}), nil
	}

	if err := DecodeLoop(context.Background(), newDecoder, encodedCh, pcmOutCh); err != nil {
		t.Fatal(err)
	}

	var got []audio.MediaPCMFrame
	for frame := range pcmOutCh {
		got = append(got, frame)
	}

	want := []audio.MediaPCMFrame{
		{SenderID: 10, Sequence: 1, Samples: []int16{1, 1}},
		{SenderID: 20, Sequence: 7, Samples: []int16{2, 1}},
		{SenderID: 10, Sequence: 2, Samples: []int16{1, 2}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded frames = %+v, want %+v", got, want)
	}
	if created != 2 {
		t.Fatalf("created decoders = %d, want 2", created)
	}
}

func TestStreamDecodersRemoveInactive(t *testing.T) {
	now := time.Now()
	created := 0
	decoders := newStreamDecoders(func() (audio.Decoder, error) {
		created++
		return decoderFunc(func([]byte) ([]int16, error) { return nil, nil }), nil
	})

	if _, err := decoders.decodeAt(
		audio.MediaFrame{SenderID: 10},
		now.Add(-DefaultStreamIdleTimeout),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := decoders.decodeAt(
		audio.MediaFrame{SenderID: 20},
		now.Add(-time.Second),
	); err != nil {
		t.Fatal(err)
	}

	if removed := decoders.RemoveInactive(now, DefaultStreamIdleTimeout); removed != 1 {
		t.Fatalf("RemoveInactive() = %d, want 1", removed)
	}
	if _, ok := decoders.bySender[10]; ok {
		t.Fatal("inactive decoder was not removed")
	}
	if _, ok := decoders.bySender[20]; !ok {
		t.Fatal("active decoder was removed")
	}

	if _, err := decoders.decodeAt(audio.MediaFrame{SenderID: 10}, now); err != nil {
		t.Fatal(err)
	}
	if created != 3 {
		t.Fatalf("created decoders = %d, want 3 after inactive sender returned", created)
	}
}
