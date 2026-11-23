package client

import (
	"context"
	"errors"
	"time"

	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
)

// NoticeHandler receives server notices. It runs on the control loop and
// must not block.
type NoticeHandler func(protocol.Notice)

type noticeHook struct{ handle NoticeHandler }

// SetNoticeHandler installs the receiver of PacketNotice. A nil handler drops them.
func (s *State) SetNoticeHandler(handler NoticeHandler) {
	s.noticeHandler.Store(&noticeHook{handle: handler})
}

// Notice delivers one decoded notice to the installed handler.
func (s *State) Notice(notice protocol.Notice) {
	if hook := s.noticeHandler.Load(); hook != nil && hook.handle != nil {
		hook.handle(notice)
	}
}

// SetNoticePrefs opts the session in to notices. A server that predates them
// answers with an error, which callers treat as "not supported".
func SetNoticePrefs(ctx context.Context, conn *udp.ClientPacketConn, state *State, flags byte) error {
	response, err := DoRequest(ctx, conn, state, protocol.VoicePacket{Type: protocol.PacketNoticePrefs, Payload: protocol.EncodeNoticePrefs(flags)}, 3*time.Second)
	if err != nil {
		return err
	}
	if response.Type != protocol.PacketNoticePrefsAck {
		return errors.New("invalid notice preferences response")
	}
	got, err := protocol.DecodeNoticePrefs(response.Payload)
	if err != nil || got != flags {
		return errors.New("invalid notice preferences acknowledgement")
	}
	return nil
}
