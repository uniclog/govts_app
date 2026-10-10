package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"

	"uniclog.io/sonoryx/internal/domain"
)

const (
	JoinChannelRequestSize = 8
	JoinChannelAckSize     = 16
)

func EncodeJoinChannelRequest(channelID domain.ChannelID) ([]byte, error) {
	if channelID == 0 {
		return nil, errors.New("channel ID must not be zero")
	}
	payload := make([]byte, JoinChannelRequestSize)
	binary.BigEndian.PutUint64(payload, uint64(channelID))
	return payload, nil
}

func DecodeJoinChannelRequest(payload []byte) (domain.ChannelID, error) {
	if len(payload) != JoinChannelRequestSize {
		return 0, fmt.Errorf("invalid join request size: got %d, want %d", len(payload), JoinChannelRequestSize)
	}
	id := domain.ChannelID(binary.BigEndian.Uint64(payload))
	if id == 0 {
		return 0, errors.New("channel ID must not be zero")
	}
	return id, nil
}

func EncodeJoinChannelAck(channelID domain.ChannelID, revision domain.StateRevision) ([]byte, error) {
	if channelID == 0 || revision == 0 {
		return nil, errors.New("channel ID and revision must not be zero")
	}
	payload := make([]byte, JoinChannelAckSize)
	binary.BigEndian.PutUint64(payload[:8], uint64(channelID))
	binary.BigEndian.PutUint64(payload[8:], uint64(revision))
	return payload, nil
}

func DecodeJoinChannelAck(payload []byte) (domain.ChannelID, domain.StateRevision, error) {
	if len(payload) != JoinChannelAckSize {
		return 0, 0, fmt.Errorf("invalid join ack size: got %d, want %d", len(payload), JoinChannelAckSize)
	}
	id := domain.ChannelID(binary.BigEndian.Uint64(payload[:8]))
	revision := domain.StateRevision(binary.BigEndian.Uint64(payload[8:]))
	if id == 0 || revision == 0 {
		return 0, 0, errors.New("channel ID and revision must not be zero")
	}
	return id, revision, nil
}
