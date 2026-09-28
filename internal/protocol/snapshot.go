package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"uniclog.io/govts/internal/domain"
)

const (
	SnapshotSchemaVersion      uint8  = 5
	SnapshotRequestSize               = 16
	SnapshotResponseHeaderSize        = 18
	MaxSnapshotPageItems       uint16 = 32
)

type SnapshotKind uint8

const (
	SnapshotKindMetadata SnapshotKind = iota + 1
	SnapshotKindChannels
	SnapshotKindParticipants
	SnapshotKindScreenStreams
)

type SnapshotStatus uint8

const (
	SnapshotStatusOK SnapshotStatus = iota + 1
	SnapshotStatusRevisionChanged
)

type SnapshotRequest struct {
	Kind             SnapshotKind
	ExpectedRevision domain.StateRevision
	Offset           uint32
	Limit            uint16
}
type SnapshotResponse struct {
	Kind              SnapshotKind
	Status            SnapshotStatus
	Revision          domain.StateRevision
	NextOffset        uint32
	HasMore           bool
	ServerInfo        domain.ServerInfo
	ChannelCount      uint32
	ParticipantCount  uint32
	ScreenStreamCount uint32
	Channels          []domain.Channel
	Participants      []domain.Participant
	ScreenStreams     []domain.ScreenStream
}

func EncodeSnapshotRequest(request SnapshotRequest) ([]byte, error) {
	if err := validateSnapshotRequest(request); err != nil {
		return nil, err
	}
	p := make([]byte, SnapshotRequestSize)
	p[0] = SnapshotSchemaVersion
	p[1] = byte(request.Kind)
	binary.BigEndian.PutUint64(p[2:10], uint64(request.ExpectedRevision))
	binary.BigEndian.PutUint32(p[10:14], request.Offset)
	binary.BigEndian.PutUint16(p[14:16], request.Limit)
	return p, nil
}
func DecodeSnapshotRequest(p []byte) (SnapshotRequest, error) {
	if len(p) != SnapshotRequestSize {
		return SnapshotRequest{}, fmt.Errorf("invalid snapshot request size: %d", len(p))
	}
	if p[0] != SnapshotSchemaVersion {
		return SnapshotRequest{}, fmt.Errorf("unsupported snapshot version: %d", p[0])
	}
	r := SnapshotRequest{Kind: SnapshotKind(p[1]), ExpectedRevision: domain.StateRevision(binary.BigEndian.Uint64(p[2:10])), Offset: binary.BigEndian.Uint32(p[10:14]), Limit: binary.BigEndian.Uint16(p[14:16])}
	if err := validateSnapshotRequest(r); err != nil {
		return SnapshotRequest{}, err
	}
	return r, nil
}
func validateSnapshotRequest(r SnapshotRequest) error {
	switch r.Kind {
	case SnapshotKindMetadata:
		if r.ExpectedRevision != 0 || r.Offset != 0 || r.Limit != 0 {
			return errors.New("metadata request must have zero revision, offset, and limit")
		}
	case SnapshotKindChannels, SnapshotKindParticipants, SnapshotKindScreenStreams:
		if r.ExpectedRevision == 0 {
			return errors.New("page request revision must not be zero")
		}
		if r.Limit == 0 || r.Limit > MaxSnapshotPageItems {
			return fmt.Errorf("page limit must be 1..%d", MaxSnapshotPageItems)
		}
	default:
		return fmt.Errorf("unknown snapshot kind: %d", r.Kind)
	}
	return nil
}

func EncodedChannelSize(c domain.Channel) int {
	return 46 + len(c.Name) + len(c.Topic) + len(c.Description)
}
func EncodedParticipantSize(p domain.Participant) int { return 19 + len(p.DisplayName) }
func EncodedScreenStreamSize(domain.ScreenStream) int { return 24 }

func EncodeSnapshotResponse(r SnapshotResponse) ([]byte, error) {
	if r.Revision == 0 {
		return nil, errors.New("snapshot revision must not be zero")
	}
	if r.Kind < SnapshotKindMetadata || r.Kind > SnapshotKindScreenStreams {
		return nil, fmt.Errorf("unknown snapshot kind: %d", r.Kind)
	}
	if r.Status != SnapshotStatusOK && r.Status != SnapshotStatusRevisionChanged {
		return nil, fmt.Errorf("unknown snapshot status: %d", r.Status)
	}
	if r.Status == SnapshotStatusRevisionChanged {
		if r.Kind == SnapshotKindMetadata || r.NextOffset != 0 || r.HasMore || len(r.Channels) != 0 || len(r.Participants) != 0 {
			return nil, errors.New("non-canonical revision-changed response")
		}
	} else if r.HasMore == (r.NextOffset == 0) {
		return nil, errors.New("next offset and has-more are inconsistent")
	}
	body := make([]byte, 0)
	var count int
	if r.Status == SnapshotStatusOK {
		switch r.Kind {
		case SnapshotKindMetadata:
			if r.NextOffset != 0 || r.HasMore || len(r.Channels) != 0 || len(r.Participants) != 0 || len(r.ScreenStreams) != 0 {
				return nil, errors.New("non-canonical metadata response")
			}
			var err error
			body, err = appendString(body, r.ServerInfo.Name, domain.MaxServerNameBytes)
			if err != nil {
				return nil, err
			}
			if r.ServerInfo.Name == "" || strings.TrimSpace(r.ServerInfo.Name) != r.ServerInfo.Name {
				return nil, errors.New("invalid server name")
			}
			body = binary.BigEndian.AppendUint16(body, r.ServerInfo.MediaPort)
			body = binary.BigEndian.AppendUint64(body, uint64(r.ServerInfo.DefaultChannelID))
			body = binary.BigEndian.AppendUint32(body, r.ChannelCount)
			body = binary.BigEndian.AppendUint32(body, r.ParticipantCount)
			body = binary.BigEndian.AppendUint32(body, r.ScreenStreamCount)
		case SnapshotKindChannels:
			if len(r.Participants) != 0 {
				return nil, errors.New("channels response contains participants")
			}
			count = len(r.Channels)
			for _, item := range r.Channels {
				var err error
				body, err = appendChannel(body, item)
				if err != nil {
					return nil, err
				}
			}
		case SnapshotKindParticipants:
			if len(r.Channels) != 0 || len(r.ScreenStreams) != 0 {
				return nil, errors.New("participants response contains other items")
			}
			count = len(r.Participants)
			for _, item := range r.Participants {
				var err error
				body, err = appendParticipant(body, item)
				if err != nil {
					return nil, err
				}
			}
		case SnapshotKindScreenStreams:
			if len(r.Channels) != 0 || len(r.Participants) != 0 {
				return nil, errors.New("screen streams response contains other items")
			}
			count = len(r.ScreenStreams)
			for _, item := range r.ScreenStreams {
				body = appendScreenStream(body, item)
			}
		}
	}
	if count > int(MaxSnapshotPageItems) {
		return nil, errors.New("too many snapshot items")
	}
	p := make([]byte, SnapshotResponseHeaderSize)
	p[0] = SnapshotSchemaVersion
	p[1] = byte(r.Kind)
	p[2] = byte(r.Status)
	binary.BigEndian.PutUint64(p[3:11], uint64(r.Revision))
	binary.BigEndian.PutUint32(p[11:15], r.NextOffset)
	if r.HasMore {
		p[15] = 1
	}
	binary.BigEndian.PutUint16(p[16:18], uint16(count))
	p = append(p, body...)
	if len(p) > MaxPayloadSize {
		return nil, fmt.Errorf("%w: snapshot response is %d bytes", ErrPayloadTooLarge, len(p))
	}
	return p, nil
}

func DecodeSnapshotResponse(p []byte) (SnapshotResponse, error) {
	if len(p) < SnapshotResponseHeaderSize {
		return SnapshotResponse{}, errors.New("snapshot response too short")
	}
	if p[0] != SnapshotSchemaVersion {
		return SnapshotResponse{}, fmt.Errorf("unsupported snapshot version: %d", p[0])
	}
	if p[15] > 1 {
		return SnapshotResponse{}, errors.New("invalid has-more boolean")
	}
	r := SnapshotResponse{Kind: SnapshotKind(p[1]), Status: SnapshotStatus(p[2]), Revision: domain.StateRevision(binary.BigEndian.Uint64(p[3:11])), NextOffset: binary.BigEndian.Uint32(p[11:15]), HasMore: p[15] == 1}
	count := int(binary.BigEndian.Uint16(p[16:18]))
	if r.Revision == 0 {
		return SnapshotResponse{}, errors.New("zero snapshot revision")
	}
	if count > int(MaxSnapshotPageItems) {
		return SnapshotResponse{}, errors.New("too many snapshot items")
	}
	body := p[18:]
	if r.Status == SnapshotStatusRevisionChanged {
		if (r.Kind != SnapshotKindChannels && r.Kind != SnapshotKindParticipants && r.Kind != SnapshotKindScreenStreams) || r.NextOffset != 0 || r.HasMore || count != 0 || len(body) != 0 {
			return SnapshotResponse{}, errors.New("non-canonical revision-changed response")
		}
		return r, nil
	}
	if r.Status != SnapshotStatusOK {
		return SnapshotResponse{}, fmt.Errorf("unknown snapshot status: %d", r.Status)
	}
	if r.HasMore == (r.NextOffset == 0) {
		return SnapshotResponse{}, errors.New("next offset and has-more are inconsistent")
	}
	var err error
	switch r.Kind {
	case SnapshotKindMetadata:
		if count != 0 || r.NextOffset != 0 || r.HasMore {
			return SnapshotResponse{}, errors.New("non-canonical metadata response")
		}
		r.ServerInfo.Name, body, err = takeString(body, domain.MaxServerNameBytes)
		if err == nil && (r.ServerInfo.Name == "" || strings.TrimSpace(r.ServerInfo.Name) != r.ServerInfo.Name) {
			err = errors.New("invalid server name")
		}
		if err == nil && len(body) >= 22 {
			r.ServerInfo.MediaPort = binary.BigEndian.Uint16(body[:2])
			r.ServerInfo.DefaultChannelID = domain.ChannelID(binary.BigEndian.Uint64(body[2:10]))
			r.ChannelCount = binary.BigEndian.Uint32(body[10:14])
			r.ParticipantCount = binary.BigEndian.Uint32(body[14:18])
			r.ScreenStreamCount = binary.BigEndian.Uint32(body[18:22])
			body = body[22:]
		} else if err == nil {
			err = errors.New("metadata body too short")
		}
	case SnapshotKindChannels:
		r.Channels = make([]domain.Channel, 0, count)
		for i := 0; i < count && err == nil; i++ {
			var item domain.Channel
			item, body, err = takeChannel(body)
			r.Channels = append(r.Channels, item)
		}
	case SnapshotKindParticipants:
		r.Participants = make([]domain.Participant, 0, count)
		for i := 0; i < count && err == nil; i++ {
			var item domain.Participant
			item, body, err = takeParticipant(body)
			r.Participants = append(r.Participants, item)
		}
	case SnapshotKindScreenStreams:
		r.ScreenStreams = make([]domain.ScreenStream, 0, count)
		for i := 0; i < count && err == nil; i++ {
			var item domain.ScreenStream
			item, body, err = takeScreenStream(body)
			r.ScreenStreams = append(r.ScreenStreams, item)
		}
	default:
		return SnapshotResponse{}, fmt.Errorf("unknown snapshot kind: %d", r.Kind)
	}
	if err != nil {
		return SnapshotResponse{}, err
	}
	if len(body) != 0 {
		return SnapshotResponse{}, errors.New("trailing snapshot response bytes")
	}
	return r, nil
}

func appendScreenStream(dst []byte, s domain.ScreenStream) []byte {
	dst = binary.BigEndian.AppendUint64(dst, uint64(s.ID))
	dst = binary.BigEndian.AppendUint64(dst, s.OwnerSessionID)
	return binary.BigEndian.AppendUint64(dst, uint64(s.ChannelID))
}

func takeScreenStream(p []byte) (domain.ScreenStream, []byte, error) {
	if len(p) < 24 {
		return domain.ScreenStream{}, nil, errors.New("screen stream item too short")
	}
	s := domain.ScreenStream{ID: domain.StreamID(binary.BigEndian.Uint64(p[:8])), OwnerSessionID: binary.BigEndian.Uint64(p[8:16]), ChannelID: domain.ChannelID(binary.BigEndian.Uint64(p[16:24]))}
	if s.ID == 0 || s.OwnerSessionID == 0 || s.ChannelID == 0 {
		return domain.ScreenStream{}, nil, errors.New("invalid screen stream item")
	}
	return s, p[24:], nil
}

func appendString(dst []byte, s string, max int) ([]byte, error) {
	if !utf8.ValidString(s) || len(s) > max {
		return nil, errors.New("invalid snapshot string")
	}
	dst = binary.BigEndian.AppendUint16(dst, uint16(len(s)))
	return append(dst, s...), nil
}
func takeString(p []byte, max int) (string, []byte, error) {
	if len(p) < 2 {
		return "", nil, errors.New("missing string length")
	}
	n := int(binary.BigEndian.Uint16(p))
	p = p[2:]
	if n > max || n > len(p) || !utf8.Valid(p[:min(n, len(p))]) {
		return "", nil, errors.New("invalid snapshot string")
	}
	return string(p[:n]), p[n:], nil
}
func appendChannel(dst []byte, c domain.Channel) ([]byte, error) {
	if err := validateSnapshotChannel(c); err != nil {
		return nil, err
	}
	dst = binary.BigEndian.AppendUint64(dst, uint64(c.ID))
	dst = binary.BigEndian.AppendUint64(dst, uint64(c.ParentID))
	dst = binary.BigEndian.AppendUint32(dst, c.Position)
	dst = binary.BigEndian.AppendUint32(dst, c.MaxUsers)
	dst = binary.BigEndian.AppendUint16(dst, c.MinJoinLevel)
	dst = append(dst, byte(c.Type), byte(c.Audio.Codec))
	dst = binary.BigEndian.AppendUint32(dst, c.Audio.SampleRate)
	dst = append(dst, c.Audio.Channels)
	dst = binary.BigEndian.AppendUint16(dst, c.Audio.FrameDurationMS)
	dst = binary.BigEndian.AppendUint32(dst, c.Audio.Bitrate)
	dst = append(dst, byte(c.Audio.Application))
	var err error
	for _, v := range []struct {
		s string
		m int
	}{{c.Name, domain.MaxChannelNameBytes}, {c.Topic, domain.MaxChannelTopicBytes}, {c.Description, domain.MaxChannelDescriptionBytes}} {
		dst, err = appendString(dst, v.s, v.m)
		if err != nil {
			return nil, err
		}
	}
	return dst, nil
}
func takeChannel(p []byte) (domain.Channel, []byte, error) {
	if len(p) < 40 {
		return domain.Channel{}, nil, errors.New("channel item too short")
	}
	c := domain.Channel{ID: domain.ChannelID(binary.BigEndian.Uint64(p[:8])), ParentID: domain.ChannelID(binary.BigEndian.Uint64(p[8:16])), Position: binary.BigEndian.Uint32(p[16:20]), MaxUsers: binary.BigEndian.Uint32(p[20:24]), MinJoinLevel: binary.BigEndian.Uint16(p[24:26]), Type: domain.ChannelType(p[26]), Audio: domain.AudioProfile{Codec: domain.AudioCodec(p[27]), SampleRate: binary.BigEndian.Uint32(p[28:32]), Channels: p[32], FrameDurationMS: binary.BigEndian.Uint16(p[33:35]), Bitrate: binary.BigEndian.Uint32(p[35:39]), Application: domain.OpusApplication(p[39])}}
	if c.ID == 0 {
		return c, nil, errors.New("zero channel ID")
	}
	p = p[40:]
	var err error
	c.Name, p, err = takeString(p, domain.MaxChannelNameBytes)
	if err == nil {
		c.Topic, p, err = takeString(p, domain.MaxChannelTopicBytes)
	}
	if err == nil {
		c.Description, p, err = takeString(p, domain.MaxChannelDescriptionBytes)
	}
	if err == nil {
		err = validateSnapshotChannel(c)
	}
	return c, p, err
}
func appendParticipant(dst []byte, p domain.Participant) ([]byte, error) {
	if err := validateSnapshotParticipant(p); err != nil {
		return nil, err
	}
	dst = binary.BigEndian.AppendUint64(dst, p.SessionID)
	dst = binary.BigEndian.AppendUint64(dst, uint64(p.ChannelID))
	dst, err := appendString(dst, p.DisplayName, domain.MaxParticipantNameBytes)
	if err != nil {
		return nil, err
	}
	return append(dst, audioStateFlags(p.Muted, p.Deafened)), nil
}
func takeParticipant(p []byte) (domain.Participant, []byte, error) {
	if len(p) < 16 {
		return domain.Participant{}, nil, errors.New("participant item too short")
	}
	v := domain.Participant{SessionID: binary.BigEndian.Uint64(p[:8]), ChannelID: domain.ChannelID(binary.BigEndian.Uint64(p[8:16]))}
	if v.SessionID == 0 {
		return v, nil, errors.New("zero participant ID")
	}
	var err error
	v.DisplayName, p, err = takeString(p[16:], domain.MaxParticipantNameBytes)
	if err == nil && len(p) < 1 {
		err = errors.New("participant item too short")
	}
	if err == nil {
		v.Muted, v.Deafened, err = audioStateFromFlags(p[0])
		p = p[1:]
	}
	if err == nil {
		err = validateSnapshotParticipant(v)
	}
	return v, p, err
}

func validateSnapshotChannel(c domain.Channel) error {
	if c.ID == 0 {
		return errors.New("zero channel ID")
	}
	if c.Name == "" || strings.TrimSpace(c.Name) != c.Name {
		return errors.New("invalid channel name")
	}
	if c.Type != domain.ChannelTypePermanent || domain.ValidateAudioProfile(c.Audio) != nil {
		return errors.New("unsupported channel properties")
	}
	return nil
}
func validateSnapshotParticipant(p domain.Participant) error {
	if p.SessionID == 0 {
		return errors.New("zero participant ID")
	}
	if p.DisplayName == "" || strings.TrimSpace(p.DisplayName) != p.DisplayName {
		return errors.New("invalid participant name")
	}
	return nil
}
