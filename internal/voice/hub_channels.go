package voice

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
)

// SetDefaultChannel selects the unrestricted channel used for every new connection.
func (h *Hub) SetDefaultChannel(id domain.ChannelID) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	channel := h.channels[id]
	if channel == nil || channel.MinJoinLevel != 0 || channel.MaxUsers != 0 {
		return fmt.Errorf("%w: default channel must exist, have join level 0 and no user limit", ErrInvalidChannel)
	}
	h.serverInfo.DefaultChannelID = id
	return nil
}

func (h *Hub) JoinChannel(id uint64, channelID domain.ChannelID) error {
	_, err := h.JoinChannelWithRevision(id, channelID)
	return err
}

func (h *Hub) JoinChannelWithRevision(id uint64, channelID domain.ChannelID) (domain.StateRevision, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if err := h.joinChannelLocked(id, channelID); err != nil {
		return 0, err
	}
	return h.revision, nil
}

func (h *Hub) joinChannelLocked(id uint64, channelID domain.ChannelID) error {
	session, ok := h.sessions[id]
	if !ok {
		return ErrSessionNotFound
	}
	channel, ok := h.channels[channelID]
	if !ok {
		return fmt.Errorf("%w: %d", ErrChannelNotFound, channelID)
	}
	if session.ChannelID == channelID {
		return nil
	}
	if session.JoinLevel < channel.MinJoinLevel {
		return ErrChannelForbidden
	}
	if channel.MaxUsers > 0 && h.channelMemberCountLocked(channelID) >= channel.MaxUsers {
		return fmt.Errorf("%w: %q", ErrChannelFull, channel.Name)
	}
	if h.channelMemberCountLocked(channelID) >= protocol.MaxVoiceSources {
		return fmt.Errorf("%w: voice source limit %d", ErrChannelFull, protocol.MaxVoiceSources)
	}
	if err := h.advanceVoiceIdentityLocked(session, true, true); err != nil {
		return err
	}

	h.stopScreenShareByOwnerLocked(id)
	session.ChannelID = channelID
	h.revision++
	if session.voiceControl != nil {
		session.voiceControl.profileRevision = uint64(h.revision)
	}
	h.emitLocked(domain.StateEvent{Kind: domain.ParticipantMoved, SessionID: id, ChannelID: channelID})
	return nil
}

func (h *Hub) CreateChannel(channel domain.Channel) (domain.Channel, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if channel.ID != 0 {
		return domain.Channel{}, fmt.Errorf(
			"%w: channel ID is assigned by the hub",
			ErrInvalidChannel,
		)
	}
	if channel.Type == 0 {
		channel.Type = domain.ChannelTypePermanent
	}
	if channel.Audio == (domain.AudioProfile{}) {
		channel.Audio = domain.DefaultAudioProfile()
	}
	if err := validateChannel(channel); err != nil {
		return domain.Channel{}, err
	}
	if err := h.validateChannelParentLocked(channel.ParentID); err != nil {
		return domain.Channel{}, err
	}
	if h.channelNameExistsLocked(channel.ParentID, channel.Name) {
		return domain.Channel{}, fmt.Errorf(
			"%w: %q",
			ErrChannelNameTaken,
			channel.Name,
		)
	}

	id, err := h.nextChannelIDLocked()
	if err != nil {
		return domain.Channel{}, err
	}
	channel.ID = id
	h.channels[id] = cloneChannel(channel)
	h.revision++
	return channel, nil
}

// RestoreChannel inserts a persisted channel with its original ID at startup.
// Parents must be restored before children.
func (h *Hub) RestoreChannel(channel domain.Channel) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if channel.ID == 0 || channel.Type != domain.ChannelTypePermanent {
		return ErrInvalidChannel
	}
	if _, exists := h.channels[channel.ID]; exists {
		return ErrInvalidChannel
	}
	if err := validateChannel(channel); err != nil {
		return err
	}
	if err := h.validateChannelParentLocked(channel.ParentID); err != nil {
		return err
	}
	if h.channelNameExistsLocked(channel.ParentID, channel.Name) {
		return ErrChannelNameTaken
	}
	h.channels[channel.ID] = cloneChannel(channel)
	if channel.ID >= h.nextChannelID {
		h.nextChannelID = channel.ID + 1
	}
	h.revision++
	return nil
}

// ApplyJoinLevel changes the effective level of every active session for an
// account. Sessions that no longer qualify for their channel are disconnected.
func (h *Hub) ApplyJoinLevel(userID int64, level uint16) []uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	var removed []uint64
	for id, session := range h.sessions {
		if session.UserID != userID {
			continue
		}
		session.JoinLevel = level
		if channel := h.channels[session.ChannelID]; channel != nil && level < channel.MinJoinLevel {
			delete(h.sessions, id)
			h.stopScreenShareByOwnerLocked(id)
			h.revision++
			h.emitLocked(domain.StateEvent{Kind: domain.ParticipantLeft, SessionID: id})
			removed = append(removed, id)
		}
	}
	sort.Slice(removed, func(i, j int) bool { return removed[i] < removed[j] })
	return removed
}

func (h *Hub) SetChannelJoinLevel(channelID domain.ChannelID, level uint16) ([]uint64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	channel := h.channels[channelID]
	if channel == nil {
		return nil, ErrChannelNotFound
	}
	if channelID == h.serverInfo.DefaultChannelID && level != 0 {
		return nil, fmt.Errorf("%w: default channel must have join level 0", ErrInvalidChannel)
	}
	if channel.MinJoinLevel == level {
		return nil, nil
	}
	channel.MinJoinLevel = level
	h.revision++
	h.emitLocked(domain.StateEvent{Kind: domain.ChannelUpdated, Channel: *cloneChannel(*channel)})
	return h.removeBelowJoinLevelLocked(channelID, level), nil
}

// removeBelowJoinLevelLocked disconnects the sessions in channelID whose
// level no longer reaches level.
func (h *Hub) removeBelowJoinLevelLocked(channelID domain.ChannelID, level uint16) []uint64 {
	var removed []uint64
	for id, session := range h.sessions {
		if session.ChannelID != channelID || session.JoinLevel >= level {
			continue
		}
		delete(h.sessions, id)
		h.stopScreenShareByOwnerLocked(id)
		h.revision++
		h.emitLocked(domain.StateEvent{Kind: domain.ParticipantLeft, SessionID: id})
		removed = append(removed, id)
	}
	sort.Slice(removed, func(i, j int) bool { return removed[i] < removed[j] })
	return removed
}

func (h *Hub) GetChannel(id domain.ChannelID) (domain.Channel, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	channel, ok := h.channels[id]
	if !ok {
		return domain.Channel{}, false
	}
	return *cloneChannel(*channel), true
}

func (h *Hub) ListChannels() []domain.Channel {
	h.mu.RLock()
	defer h.mu.RUnlock()

	return h.channelsSnapshotLocked()
}

func (h *Hub) Members(channelID domain.ChannelID) []string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	members := make([]string, 0)
	for _, session := range h.sessions {
		if session.ChannelID == channelID {
			members = append(members, session.Name)
		}
	}
	sort.Strings(members)
	return members
}

func (h *Hub) SessionsInChannel(channelID domain.ChannelID) []Session {
	h.mu.RLock()
	defer h.mu.RUnlock()

	sessions := make([]Session, 0)
	for _, session := range h.sessions {
		if session.ChannelID == channelID {
			sessions = append(sessions, *cloneSession(session))
		}
	}
	sortSessionsByID(sessions)
	return sessions
}

func (h *Hub) Recipients(channelID domain.ChannelID, senderID uint64) []Session {
	h.mu.RLock()
	defer h.mu.RUnlock()

	sessions := make([]Session, 0)
	for _, session := range h.sessions {
		if session.ChannelID == channelID && session.ID != senderID {
			sessions = append(sessions, *cloneSession(session))
		}
	}
	sortSessionsByID(sessions)
	return sessions
}

func (h *Hub) RecipientsFor(senderID uint64) ([]Session, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	sender, ok := h.sessions[senderID]
	if !ok {
		return nil, fmt.Errorf("session %d not found", senderID)
	}
	if sender.ChannelID == 0 {
		return nil, fmt.Errorf("session %d: %w", senderID, ErrSessionNotInChannel)
	}

	recipients := make([]Session, 0)
	for _, session := range h.sessions {
		if session.ChannelID == sender.ChannelID && session.ID != senderID {
			recipients = append(recipients, *cloneSession(session))
		}
	}
	sortSessionsByID(recipients)
	return recipients, nil
}

func validateChannel(channel domain.Channel) error {
	if !validChannelName(channel.Name) {
		return fmt.Errorf(
			"%w: channel name must be valid UTF-8, trimmed, and 1..%d bytes",
			ErrInvalidChannel,
			domain.MaxChannelNameBytes,
		)
	}
	if !utf8.ValidString(channel.Topic) || len(channel.Topic) > domain.MaxChannelTopicBytes {
		return fmt.Errorf(
			"%w: channel topic exceeds %d bytes or is not valid UTF-8",
			ErrInvalidChannel,
			domain.MaxChannelTopicBytes,
		)
	}
	if !utf8.ValidString(channel.Description) ||
		len(channel.Description) > domain.MaxChannelDescriptionBytes {
		return fmt.Errorf(
			"%w: channel description exceeds %d bytes or is not valid UTF-8",
			ErrInvalidChannel,
			domain.MaxChannelDescriptionBytes,
		)
	}
	if channel.Type != domain.ChannelTypePermanent {
		return fmt.Errorf("%w: unsupported channel type %d", ErrInvalidChannel, channel.Type)
	}
	if err := domain.ValidateAudioProfile(channel.Audio); err != nil {
		return fmt.Errorf("%w: unsupported audio profile: %v", ErrInvalidChannel, err)
	}
	return nil
}

func validChannelName(name string) bool {
	return utf8.ValidString(name) &&
		!hasNameControlCharacters(name) &&
		name != "" &&
		len(name) <= domain.MaxChannelNameBytes &&
		strings.TrimSpace(name) == name
}

// hasNameControlCharacters reports control and format (Cf) characters. Bidi
// overrides and zero-width marks could make one name pass for another.
func hasNameControlCharacters(name string) bool {
	return strings.ContainsFunc(name, func(r rune) bool {
		return unicode.IsControl(r) || r == '\u2028' || r == '\u2029' || unicode.Is(unicode.Cf, r)
	})
}

func (h *Hub) validateChannelParentLocked(parentID domain.ChannelID) error {
	depth := 1
	seen := make(map[domain.ChannelID]struct{})
	for parentID != 0 {
		if _, duplicate := seen[parentID]; duplicate {
			return fmt.Errorf("%w: channel hierarchy contains a cycle", ErrInvalidChannel)
		}
		seen[parentID] = struct{}{}

		parent, ok := h.channels[parentID]
		if !ok {
			return fmt.Errorf("%w: parent %d", ErrChannelNotFound, parentID)
		}
		depth++
		if depth > domain.MaxChannelDepth {
			return fmt.Errorf(
				"%w: channel depth exceeds %d",
				ErrInvalidChannel,
				domain.MaxChannelDepth,
			)
		}
		parentID = parent.ParentID
	}
	return nil
}

func (h *Hub) channelNameExistsLocked(parentID domain.ChannelID, name string) bool {
	for _, channel := range h.channels {
		if channel.ParentID == parentID && strings.EqualFold(channel.Name, name) {
			return true
		}
	}
	return false
}

func (h *Hub) channelMemberCountLocked(channelID domain.ChannelID) uint32 {
	var count uint32
	for _, session := range h.sessions {
		if session.ChannelID == channelID {
			count++
		}
	}
	return count
}

func (h *Hub) nextChannelIDLocked() (domain.ChannelID, error) {
	for h.nextChannelID != 0 {
		id := h.nextChannelID
		h.nextChannelID++
		if _, exists := h.channels[id]; !exists {
			return id, nil
		}
	}
	return 0, errors.New("channel ID space exhausted")
}

func cloneChannel(channel domain.Channel) *domain.Channel {
	clone := channel
	return &clone
}

func (h *Hub) channelsSnapshotLocked() []domain.Channel {
	channels := make([]domain.Channel, 0, len(h.channels))
	for _, channel := range h.channels {
		channels = append(channels, *cloneChannel(*channel))
	}
	sort.Slice(channels, func(i int, j int) bool {
		if channels[i].ParentID != channels[j].ParentID {
			return channels[i].ParentID < channels[j].ParentID
		}
		if channels[i].Position != channels[j].Position {
			return channels[i].Position < channels[j].Position
		}
		return channels[i].ID < channels[j].ID
	})
	return channels
}
