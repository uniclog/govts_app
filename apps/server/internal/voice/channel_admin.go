package voice

import (
	"fmt"
	"sort"
	"strings"

	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
)

// MaxChannels matches the client snapshot limit: a larger tree could not be
// loaded by clients.
const MaxChannels = 4096

// PrepareChannel validates a channel created at runtime, fills the default
// type and audio profile and returns it with the ID AddChannel will use.
// Nothing changes until AddChannel, so durable storage can be written first.
func (h *Hub) PrepareChannel(channel domain.Channel) (domain.Channel, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if channel.ID != 0 {
		return domain.Channel{}, fmt.Errorf("%w: channel ID is assigned by the hub", ErrInvalidChannel)
	}
	if channel.Type == 0 {
		channel.Type = domain.ChannelTypePermanent
	}
	if channel.Audio == (domain.AudioProfile{}) {
		channel.Audio = domain.DefaultAudioProfile()
	}
	if len(h.channels) >= MaxChannels {
		return domain.Channel{}, fmt.Errorf("%w: at most %d channels", ErrInvalidChannel, MaxChannels)
	}
	if err := h.checkNewChannelLocked(channel); err != nil {
		return domain.Channel{}, err
	}
	for id := h.nextChannelID; id != 0; id++ {
		if _, exists := h.channels[id]; !exists {
			channel.ID = id
			return channel, nil
		}
	}
	return domain.Channel{}, fmt.Errorf("%w: channel ID space exhausted", ErrInvalidChannel)
}

func (h *Hub) checkNewChannelLocked(channel domain.Channel) error {
	if err := validateChannel(channel); err != nil {
		return err
	}
	if err := h.validateChannelParentLocked(channel.ParentID); err != nil {
		return err
	}
	if h.channelNameExistsLocked(channel.ParentID, channel.Name) {
		return fmt.Errorf("%w: %q", ErrChannelNameTaken, channel.Name)
	}
	return nil
}

// AddChannel inserts a channel returned by PrepareChannel and announces it.
func (h *Hub) AddChannel(channel domain.Channel) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if channel.ID == 0 {
		return ErrInvalidChannel
	}
	if _, exists := h.channels[channel.ID]; exists {
		return fmt.Errorf("%w: channel %d already exists", ErrInvalidChannel, channel.ID)
	}
	if len(h.channels) >= MaxChannels {
		return fmt.Errorf("%w: at most %d channels", ErrInvalidChannel, MaxChannels)
	}
	if err := h.checkNewChannelLocked(channel); err != nil {
		return err
	}
	h.channels[channel.ID] = cloneChannel(channel)
	if channel.ID >= h.nextChannelID {
		h.nextChannelID = channel.ID + 1
	}
	h.revision++
	h.emitLocked(domain.StateEvent{Kind: domain.ChannelUpdated, Channel: channel})
	return nil
}

// CheckChannelUpdate validates replacing the settings and place of an existing
// channel without changing Hub state. It returns the current channel and the
// update with an unset type or audio profile taken from it.
func (h *Hub) CheckChannelUpdate(channel domain.Channel) (current, next domain.Channel, err error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.checkChannelUpdateLocked(channel)
}

func (h *Hub) checkChannelUpdateLocked(channel domain.Channel) (domain.Channel, domain.Channel, error) {
	existing := h.channels[channel.ID]
	if channel.ID == 0 || existing == nil {
		return domain.Channel{}, domain.Channel{}, fmt.Errorf("%w: %d", ErrChannelNotFound, channel.ID)
	}
	current := *cloneChannel(*existing)
	if channel.Type == 0 {
		channel.Type = current.Type
	}
	if channel.Audio == (domain.AudioProfile{}) {
		channel.Audio = current.Audio
	}
	if err := validateChannel(channel); err != nil {
		return current, channel, err
	}
	// The new parent may not be the channel itself or one of its children.
	for id := channel.ParentID; id != 0; {
		if id == channel.ID {
			return current, channel, fmt.Errorf("%w: a channel cannot be moved into itself", ErrInvalidChannel)
		}
		parent := h.channels[id]
		if parent == nil {
			return current, channel, fmt.Errorf("%w: parent %d", ErrChannelNotFound, id)
		}
		id = parent.ParentID
	}
	if err := h.validateChannelParentLocked(channel.ParentID); err != nil {
		return current, channel, err
	}
	if h.channelDepthLocked(channel.ParentID)+h.subtreeHeightLocked(channel.ID) > domain.MaxChannelDepth {
		return current, channel, fmt.Errorf("%w: channel depth exceeds %d", ErrInvalidChannel, domain.MaxChannelDepth)
	}
	for _, sibling := range h.channels {
		if sibling.ID != channel.ID && sibling.ParentID == channel.ParentID && strings.EqualFold(sibling.Name, channel.Name) {
			return current, channel, fmt.Errorf("%w: %q", ErrChannelNameTaken, channel.Name)
		}
	}
	if channel.ID == h.serverInfo.DefaultChannelID && (channel.MinJoinLevel != 0 || channel.MaxUsers != 0) {
		return current, channel, fmt.Errorf("%w: the default channel must stay open to everyone", ErrInvalidChannel)
	}
	return current, channel, nil
}

// UpdateChannel replaces the settings and place of a channel and announces
// it. A raised join level disconnects members below it, as
// SetChannelJoinLevel does; a lowered user limit keeps current members.
func (h *Hub) UpdateChannel(channel domain.Channel) ([]uint64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	current, next, err := h.checkChannelUpdateLocked(channel)
	if err != nil {
		return nil, err
	}
	if next == current {
		return nil, nil
	}
	*h.channels[next.ID] = next
	var removed []uint64
	if next.Audio != current.Audio {
		for _, session := range h.sessions {
			if session.ChannelID == next.ID {
				if err := h.advanceVoiceIdentityLocked(session, true, false); err != nil {
					removed = append(removed, session.ID)
				}
			}
		}
	}
	// Retiring an exhausted source publishes its own event. Allocate the
	// channel event revision afterwards so clients cannot skip this update.
	h.revision++
	if next.Audio != current.Audio {
		for _, session := range h.sessions {
			if session.ChannelID == next.ID && session.voiceControl != nil {
				session.voiceControl.profileRevision = uint64(h.revision)
			}
		}
	}
	h.emitLocked(domain.StateEvent{Kind: domain.ChannelUpdated, Channel: next})
	if next.MinJoinLevel > current.MinJoinLevel {
		removed = append(removed, h.removeBelowJoinLevelLocked(next.ID, next.MinJoinLevel)...)
	}
	sort.Slice(removed, func(i, j int) bool { return removed[i] < removed[j] })
	return removed, nil
}

// CheckChannelDelete returns the channel id if DeleteChannel may remove it:
// it exists, is not the default channel and has no subchannels.
func (h *Hub) CheckChannelDelete(id domain.ChannelID) (domain.Channel, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.checkChannelDeleteLocked(id)
}

func (h *Hub) checkChannelDeleteLocked(id domain.ChannelID) (domain.Channel, error) {
	channel := h.channels[id]
	if id == 0 || channel == nil {
		return domain.Channel{}, fmt.Errorf("%w: %d", ErrChannelNotFound, id)
	}
	if id == h.serverInfo.DefaultChannelID {
		return domain.Channel{}, fmt.Errorf("%w: the default channel cannot be deleted", ErrInvalidChannel)
	}
	for _, child := range h.channels {
		if child.ParentID == id {
			return domain.Channel{}, fmt.Errorf("%w: channel has subchannels", ErrInvalidChannel)
		}
	}
	if h.channelMemberCountLocked(h.serverInfo.DefaultChannelID)+h.channelMemberCountLocked(id) > protocol.MaxVoiceSources {
		return domain.Channel{}, fmt.Errorf("%w: voice source limit %d", ErrChannelFull, protocol.MaxVoiceSources)
	}
	return *cloneChannel(*channel), nil
}

// DeleteChannel moves the members of a channel to the default channel,
// stopping their screen shares, then removes the channel and announces it.
// It returns the moved session IDs.
func (h *Hub) DeleteChannel(id domain.ChannelID) ([]uint64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, err := h.checkChannelDeleteLocked(id); err != nil {
		return nil, err
	}
	target := h.serverInfo.DefaultChannelID
	var moved []uint64
	for sessionID, session := range h.sessions {
		if session.ChannelID == id {
			moved = append(moved, sessionID)
		}
	}
	sort.Slice(moved, func(i, j int) bool { return moved[i] < moved[j] })
	completed := moved[:0]
	for _, sessionID := range moved {
		session := h.sessions[sessionID]
		if err := h.advanceVoiceIdentityLocked(session, true, true); err != nil {
			continue
		}
		h.stopScreenShareByOwnerLocked(sessionID)
		session.ChannelID = target
		h.revision++
		if session.voiceControl != nil {
			session.voiceControl.profileRevision = uint64(h.revision)
		}
		h.emitLocked(domain.StateEvent{Kind: domain.ParticipantMoved, SessionID: sessionID, ChannelID: target})
		completed = append(completed, sessionID)
	}
	delete(h.channels, id)
	h.revision++
	h.emitLocked(domain.StateEvent{Kind: domain.ChannelDeleted, ChannelID: id})
	return completed, nil
}

// channelDepthLocked counts id and its ancestors; the root (0) has depth 0.
func (h *Hub) channelDepthLocked(id domain.ChannelID) int {
	depth := 0
	for id != 0 && depth <= domain.MaxChannelDepth {
		channel := h.channels[id]
		if channel == nil {
			break
		}
		depth++
		id = channel.ParentID
	}
	return depth
}

// subtreeHeightLocked counts the levels from id down to its deepest child.
func (h *Hub) subtreeHeightLocked(id domain.ChannelID) int {
	height := 1
	for _, channel := range h.channels {
		if channel.ParentID == id && channel.ID != id {
			if child := 1 + h.subtreeHeightLocked(channel.ID); child > height {
				height = child
			}
		}
	}
	return height
}
