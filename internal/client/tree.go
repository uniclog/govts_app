package client

import (
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"uniclog.io/sonoryx/internal/domain"
)

type ChannelLocator []string

func BuildChannelLocator(snapshot domain.ServerSnapshot, channelID domain.ChannelID) (ChannelLocator, error) {
	byID := make(map[domain.ChannelID]domain.Channel, len(snapshot.Channels))
	for _, channel := range snapshot.Channels {
		byID[channel.ID] = channel
	}
	var reversed []string
	seen := make(map[domain.ChannelID]bool)
	for channelID != 0 {
		if seen[channelID] {
			return nil, errorsTree("channel hierarchy cycle")
		}
		seen[channelID] = true
		channel, ok := byID[channelID]
		if !ok {
			return nil, fmt.Errorf("channel %d not found", channelID)
		}
		reversed = append(reversed, channel.Name)
		channelID = channel.ParentID
	}
	locator := make(ChannelLocator, len(reversed))
	for i := range reversed {
		locator[len(reversed)-1-i] = reversed[i]
	}
	return locator, nil
}

func ResolveChannelLocator(snapshot domain.ServerSnapshot, locator ChannelLocator) (domain.ChannelID, error) {
	if len(locator) == 0 {
		return 0, errorsTree("channel locator is empty")
	}
	parent := domain.ChannelID(0)
	for _, segment := range locator {
		var found domain.ChannelID
		for _, channel := range snapshot.Channels {
			if channel.ParentID == parent && strings.EqualFold(channel.Name, segment) {
				if found != 0 {
					return 0, fmt.Errorf("channel path segment %q is ambiguous", segment)
				}
				found = channel.ID
			}
		}
		if found == 0 {
			return 0, fmt.Errorf("channel path %q not found", strings.Join(locator, "/"))
		}
		parent = found
	}
	return parent, nil
}

func SameChannelTopology(left, right domain.ServerSnapshot) bool {
	return strings.Join(topologyPaths(left), "\x00") == strings.Join(topologyPaths(right), "\x00")
}

func topologyPaths(snapshot domain.ServerSnapshot) []string {
	children := make(map[domain.ChannelID][]domain.Channel)
	for _, channel := range snapshot.Channels {
		children[channel.ParentID] = append(children[channel.ParentID], channel)
	}
	for parentID := range children {
		sort.Slice(children[parentID], func(i, j int) bool {
			left, right := children[parentID][i], children[parentID][j]
			if left.Position != right.Position {
				return left.Position < right.Position
			}
			return strings.ToLower(left.Name) < strings.ToLower(right.Name)
		})
	}
	var paths []string
	var walk func(domain.ChannelID, string)
	walk = func(parent domain.ChannelID, prefix string) {
		for _, channel := range children[parent] {
			path := channel.Name
			if prefix != "" {
				path = prefix + "/" + path
			}
			paths = append(paths, path+"@"+strconv.FormatUint(uint64(channel.Position), 10))
			walk(channel.ID, path)
		}
	}
	walk(0, "")
	return paths
}

func RenderServerTree(writer io.Writer, snapshot domain.ServerSnapshot, sessionID uint64, currentChannelID domain.ChannelID, stale bool) error {
	if writer == nil {
		return errorsTree("tree output is required")
	}
	if err := validateTreeSnapshot(snapshot); err != nil {
		return err
	}
	suffix := ""
	if stale {
		suffix = "  [stale]"
	}
	if _, err := fmt.Fprintf(writer, "%s  revision=%d  users=%d%s\n", terminalText(snapshot.Info.Name), snapshot.Revision, len(snapshot.Participants), suffix); err != nil {
		return err
	}
	children := make(map[domain.ChannelID][]domain.Channel)
	members := make(map[domain.ChannelID][]domain.Participant)
	var unjoined []domain.Participant
	for _, channel := range snapshot.Channels {
		children[channel.ParentID] = append(children[channel.ParentID], channel)
	}
	for parentID := range children {
		sort.Slice(children[parentID], func(i, j int) bool {
			left, right := children[parentID][i], children[parentID][j]
			if left.Position != right.Position {
				return left.Position < right.Position
			}
			return left.ID < right.ID
		})
	}
	for _, participant := range snapshot.Participants {
		if participant.ChannelID == 0 {
			unjoined = append(unjoined, participant)
		} else {
			members[participant.ChannelID] = append(members[participant.ChannelID], participant)
		}
	}
	for id := range members {
		sort.Slice(members[id], func(i, j int) bool { return members[id][i].SessionID < members[id][j].SessionID })
	}
	sort.Slice(unjoined, func(i, j int) bool { return unjoined[i].SessionID < unjoined[j].SessionID })
	var walk func(domain.ChannelID, string) error
	walk = func(parent domain.ChannelID, prefix string) error {
		items := children[parent]
		for index, channel := range items {
			last := index == len(items)-1
			branch := "├─"
			childPrefix := prefix + "│  "
			if last {
				branch = "└─"
				childPrefix = prefix + "   "
			}
			current := ""
			if channel.ID == currentChannelID {
				current = "  <- current"
			}
			if _, err := fmt.Fprintf(writer, "%s%s %s [id=%d] users=%d/%s%s\n", prefix, branch, terminalText(channel.Name), channel.ID, len(members[channel.ID]), maxUsersLabel(channel.MaxUsers), current); err != nil {
				return err
			}
			for _, participant := range members[channel.ID] {
				marker := ""
				if participant.SessionID == sessionID {
					marker = "  <- you"
				}
				if _, err := fmt.Fprintf(writer, "%s   • %s [session=%d]%s\n", childPrefix, terminalText(participant.DisplayName), participant.SessionID, marker); err != nil {
					return err
				}
			}
			if err := walk(channel.ID, childPrefix); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(0, ""); err != nil {
		return err
	}
	if len(unjoined) > 0 {
		if _, err := io.WriteString(writer, "Unjoined\n"); err != nil {
			return err
		}
		for _, participant := range unjoined {
			marker := ""
			if participant.SessionID == sessionID {
				marker = "  <- you"
			}
			if _, err := fmt.Fprintf(writer, "  • %s [session=%d]%s\n", terminalText(participant.DisplayName), participant.SessionID, marker); err != nil {
				return err
			}
		}
	}
	return nil
}

func RenderChannelMembers(writer io.Writer, snapshot domain.ServerSnapshot, channelID domain.ChannelID, sessionID uint64) error {
	var channel *domain.Channel
	for index := range snapshot.Channels {
		if snapshot.Channels[index].ID == channelID {
			channel = &snapshot.Channels[index]
			break
		}
	}
	if channel == nil {
		return fmt.Errorf("channel %d not found", channelID)
	}
	if _, err := fmt.Fprintf(writer, "joined: %s [id=%d]\nusers:\n", terminalText(channel.Name), channel.ID); err != nil {
		return err
	}
	for _, participant := range snapshot.Participants {
		if participant.ChannelID != channelID {
			continue
		}
		marker := ""
		if participant.SessionID == sessionID {
			marker = " [you]"
		}
		if _, err := fmt.Fprintf(writer, "  %s [session=%d]%s\n", terminalText(participant.DisplayName), participant.SessionID, marker); err != nil {
			return err
		}
	}
	return nil
}

func validateTreeSnapshot(snapshot domain.ServerSnapshot) error {
	channels := make(map[domain.ChannelID]domain.Channel, len(snapshot.Channels))
	for _, channel := range snapshot.Channels {
		if channel.ID == 0 {
			return errorsTree("snapshot contains channel with zero ID")
		}
		if _, exists := channels[channel.ID]; exists {
			return fmt.Errorf("snapshot contains duplicate channel ID %d", channel.ID)
		}
		channels[channel.ID] = channel
	}
	for _, channel := range snapshot.Channels {
		seen := make(map[domain.ChannelID]bool)
		for current := channel; current.ParentID != 0; {
			if seen[current.ID] {
				return fmt.Errorf("snapshot contains channel hierarchy cycle at %d", current.ID)
			}
			seen[current.ID] = true
			parent, exists := channels[current.ParentID]
			if !exists {
				return fmt.Errorf("channel %d refers to unknown parent %d", current.ID, current.ParentID)
			}
			current = parent
		}
	}
	for _, participant := range snapshot.Participants {
		if participant.ChannelID == 0 {
			continue
		}
		if _, exists := channels[participant.ChannelID]; !exists {
			return fmt.Errorf("participant %d refers to unknown channel %d", participant.SessionID, participant.ChannelID)
		}
	}
	return nil
}

func maxUsersLabel(value uint32) string {
	if value == 0 {
		return "unlimited"
	}
	return strconv.FormatUint(uint64(value), 10)
}
func errorsTree(message string) error { return fmt.Errorf("%s", message) }

// Escape untrusted text even when a snapshot comes from an older server.
func terminalText(value string) string {
	quoted := strconv.QuoteToGraphic(value)
	return quoted[1 : len(quoted)-1]
}
