package server

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/persist"
	"uniclog.io/sonoryx/internal/voice"
)

const MaxConsoleCommandBytes = 4 * 1024

type ConsoleInfo struct {
	StartedAt     time.Time
	ListenAddress string
	ConfigSource  string
}

type Console struct {
	hub              *voice.Hub
	store            *persist.Store
	notifyPrivileges func(int64, uint16, uint8)
	policyGate       *sync.Mutex
	input            io.Reader
	output           io.Writer
	info             ConsoleInfo
	now              func() time.Time
}

func (console *Console) SetPrivilegeNotifier(notify func(int64, uint16, uint8)) {
	console.notifyPrivileges = notify
}

// SetPolicyGate serializes console mutations with incoming UDP commands.
func (console *Console) SetPolicyGate(gate *sync.Mutex) { console.policyGate = gate }

func NewConsole(
	hub *voice.Hub,
	input io.Reader,
	output io.Writer,
	info ConsoleInfo,
	stores ...*persist.Store,
) *Console {
	console := &Console{
		hub:    hub,
		input:  input,
		output: output,
		info:   info,
		now:    time.Now,
	}
	if len(stores) > 0 {
		console.store = stores[0]
	}
	return console
}

func (console *Console) Run(ctx context.Context) error {
	if console == nil || console.hub == nil {
		return errors.New("console hub is required")
	}
	if console.input == nil {
		return errors.New("console input is required")
	}
	if console.output == nil {
		return errors.New("console output is required")
	}

	reader := bufio.NewReaderSize(console.input, MaxConsoleCommandBytes)
	line := make([]byte, 0, MaxConsoleCommandBytes)
	tooLong := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		fragment, isPrefix, err := reader.ReadLine()
		if err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("read server console: %w", err)
		}
		if !tooLong {
			if len(line)+len(fragment) > MaxConsoleCommandBytes {
				tooLong = true
				line = line[:0]
			} else {
				line = append(line, fragment...)
			}
		}
		if isPrefix {
			continue
		}
		if tooLong {
			if _, writeErr := fmt.Fprintf(
				console.output,
				"error: command exceeds %d bytes\n",
				MaxConsoleCommandBytes,
			); writeErr != nil {
				return writeErr
			}
		} else if len(line) > 0 {
			if executeErr := console.Execute(string(line)); executeErr != nil {
				return executeErr
			}
		}
		line = line[:0]
		tooLong = false
		if errors.Is(err, io.EOF) {
			return nil
		}
	}
}

func (console *Console) Execute(line string) (executeErr error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	command := line
	argument := ""
	if separator := strings.IndexFunc(line, unicode.IsSpace); separator >= 0 {
		command = line[:separator]
		argument = strings.TrimSpace(line[separator:])
	}
	command = strings.ToLower(command)
	defer func() {
		log.Printf("server console command: command=%q argument=%q error=%v", command, argument, executeErr)
	}()

	snapshot := console.hub.Inspect()
	now := console.now()
	switch command {
	case "help":
		if argument != "" {
			return console.writeUsage("help")
		}
		return console.writeHelp()
	case "status":
		if argument != "" {
			return console.writeUsage("status")
		}
		return console.writeStatus(snapshot, now)
	case "channels":
		if argument != "" {
			return console.writeUsage("channels")
		}
		return console.writeChannels(snapshot)
	case "channel":
		if argument == "" {
			return console.writeUsage("channel <id|name>")
		}
		if strings.HasPrefix(argument, "set-min-join-level ") {
			return console.setChannelJoinLevel(strings.TrimPrefix(argument, "set-min-join-level "))
		}
		return console.writeChannel(snapshot, argument, now)
	case "users":
		if argument != "" {
			return console.writeUsage("users")
		}
		return console.writeUsers(snapshot, now)
	case "user":
		if argument == "" || strings.ContainsAny(argument, " \t") {
			return console.writeUsage("user <session-id>")
		}
		return console.writeUser(snapshot, argument, now)
	case "accounts":
		if argument != "" {
			return console.writeUsage("accounts")
		}
		return console.writeAccounts()
	case "account":
		return console.executeAccount(argument)
	default:
		_, err := fmt.Fprintf(console.output, "error: unknown command %q; use help\n", command)
		return err
	}
}

func (console *Console) writeHelp() error {
	_, err := io.WriteString(console.output, "commands:\n"+
		"  help                  show this help\n"+
		"  status                show server summary\n"+
		"  channels              show channel tree\n"+
		"  channel <id|name>      show one channel\n"+
		"  users                 show connected users\n"+
		"  user <session-id>      show one user\n")
	if err != nil || console.store == nil {
		return err
	}
	_, err = io.WriteString(console.output,
		"  accounts              list registered accounts\n"+
			"  account <id>          show one account\n"+
			"  account set-join-level <id> <level>\n"+
			"  account set-permission <id> <kick|ban|drag> <on|off>\n"+
			"  account set-owner <id>\n"+
			"  account <ban|unban> <id>\n"+
			"  channel set-min-join-level <id> <level>\n")
	return err
}

func (console *Console) writeStatus(
	snapshot voice.OperationalSnapshot,
	now time.Time,
) error {
	unjoined := 0
	for _, session := range snapshot.Sessions {
		if session.ChannelID == 0 {
			unjoined++
		}
	}
	uptime := nonNegativeDuration(now.Sub(console.info.StartedAt)).Truncate(time.Second)
	_, err := fmt.Fprintf(
		console.output,
		"status server=%q uptime=%s listen=%q config=%q revision=%d channels=%d users=%d unjoined=%d\n",
		snapshot.ServerInfo.Name,
		uptime,
		console.info.ListenAddress,
		console.info.ConfigSource,
		snapshot.Revision,
		len(snapshot.Channels),
		len(snapshot.Sessions),
		unjoined,
	)
	return err
}

func (console *Console) writeChannels(snapshot voice.OperationalSnapshot) error {
	memberCounts := make(map[domain.ChannelID]int)
	for _, session := range snapshot.Sessions {
		memberCounts[session.ChannelID]++
	}
	children := make(map[domain.ChannelID][]domain.Channel)
	for _, channel := range snapshot.Channels {
		children[channel.ParentID] = append(children[channel.ParentID], channel)
	}

	if _, err := fmt.Fprintf(console.output, "channels revision=%d count=%d\n", snapshot.Revision, len(snapshot.Channels)); err != nil {
		return err
	}
	visited := make(map[domain.ChannelID]bool)
	var writeTree func(domain.ChannelID, int) error
	writeTree = func(parentID domain.ChannelID, depth int) error {
		for _, channel := range children[parentID] {
			if visited[channel.ID] {
				continue
			}
			visited[channel.ID] = true
			if _, err := fmt.Fprintf(
				console.output,
				"%s- id=%d name=%q parent=%d position=%d users=%d/%s default=%t min_join_level=%d type=%s audio=%s\n",
				strings.Repeat("  ", depth),
				channel.ID,
				channel.Name,
				channel.ParentID,
				channel.Position,
				memberCounts[channel.ID],
				maxUsersText(channel.MaxUsers),
				channel.ID == snapshot.ServerInfo.DefaultChannelID,
				channel.MinJoinLevel,
				channelTypeText(channel.Type),
				audioProfileText(channel.Audio),
			); err != nil {
				return err
			}
			if err := writeTree(channel.ID, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return writeTree(0, 0)
}

func (console *Console) writeChannel(
	snapshot voice.OperationalSnapshot,
	selector string,
	now time.Time,
) error {
	channel, err := findSnapshotChannel(snapshot.Channels, selector)
	if err != nil {
		_, writeErr := fmt.Fprintf(console.output, "error: %v\n", err)
		return writeErr
	}
	members := sessionsForChannel(snapshot.Sessions, channel.ID)
	if _, err := fmt.Fprintf(
		console.output,
		"channel id=%d name=%q parent=%d position=%d users=%d/%s default=%t min_join_level=%d type=%s audio=%s\n",
		channel.ID,
		channel.Name,
		channel.ParentID,
		channel.Position,
		len(members),
		maxUsersText(channel.MaxUsers),
		channel.ID == snapshot.ServerInfo.DefaultChannelID,
		channel.MinJoinLevel,
		channelTypeText(channel.Type),
		audioProfileText(channel.Audio),
	); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(console.output, "  topic=%q\n  description=%q\n", channel.Topic, channel.Description); err != nil {
		return err
	}
	for _, session := range members {
		if err := console.writeUserLine(session, channel.Name, now, "  "); err != nil {
			return err
		}
	}
	return nil
}

func (console *Console) writeUsers(
	snapshot voice.OperationalSnapshot,
	now time.Time,
) error {
	channelNames := make(map[domain.ChannelID]string, len(snapshot.Channels))
	for _, channel := range snapshot.Channels {
		channelNames[channel.ID] = channel.Name
	}
	if _, err := fmt.Fprintf(console.output, "users revision=%d count=%d\n", snapshot.Revision, len(snapshot.Sessions)); err != nil {
		return err
	}
	for _, session := range snapshot.Sessions {
		if err := console.writeUserLine(session, channelNames[session.ChannelID], now, ""); err != nil {
			return err
		}
	}
	return nil
}

func (console *Console) writeUser(
	snapshot voice.OperationalSnapshot,
	argument string,
	now time.Time,
) error {
	id, err := strconv.ParseUint(argument, 10, 64)
	if err != nil || id == 0 {
		return console.writeUsage("user <session-id>")
	}
	for _, session := range snapshot.Sessions {
		if session.ID != id {
			continue
		}
		channelName := ""
		for _, channel := range snapshot.Channels {
			if channel.ID == session.ChannelID {
				channelName = channel.Name
				break
			}
		}
		return console.writeUserLine(session, channelName, now, "user ")
	}
	_, writeErr := fmt.Fprintf(console.output, "error: session %d not found\n", id)
	return writeErr
}

func (console *Console) writeUserLine(
	session voice.Session,
	channelName string,
	now time.Time,
	prefix string,
) error {
	idle := nonNegativeDuration(now.Sub(session.LastSeen)).Truncate(time.Second)
	_, err := fmt.Fprintf(
		console.output,
		"%sid=%d name=%q channel_id=%d channel=%q endpoint=%q last_seen=%s idle=%s\n",
		prefix,
		session.ID,
		session.Name,
		session.ChannelID,
		channelName,
		udpAddressText(session.Addr),
		session.LastSeen.Local().Format(time.RFC3339Nano),
		idle,
	)
	return err
}

func (console *Console) writeUsage(usage string) error {
	_, err := fmt.Fprintf(console.output, "error: usage: %s\n", usage)
	return err
}

func findSnapshotChannel(channels []domain.Channel, selector string) (domain.Channel, error) {
	if id, err := strconv.ParseUint(selector, 10, 64); err == nil && id != 0 {
		for _, channel := range channels {
			if uint64(channel.ID) == id {
				return channel, nil
			}
		}
		return domain.Channel{}, fmt.Errorf("channel %d not found", id)
	}

	var found *domain.Channel
	for index := range channels {
		if !strings.EqualFold(channels[index].Name, selector) {
			continue
		}
		if found != nil {
			return domain.Channel{}, fmt.Errorf("channel name %q is ambiguous", selector)
		}
		found = &channels[index]
	}
	if found == nil {
		return domain.Channel{}, fmt.Errorf("channel %q not found", selector)
	}
	return *found, nil
}

func sessionsForChannel(sessions []voice.Session, channelID domain.ChannelID) []voice.Session {
	members := make([]voice.Session, 0)
	for _, session := range sessions {
		if session.ChannelID == channelID {
			members = append(members, session)
		}
	}
	sort.Slice(members, func(i int, j int) bool {
		return members[i].ID < members[j].ID
	})
	return members
}

func maxUsersText(maxUsers uint32) string {
	if maxUsers == 0 {
		return "unlimited"
	}
	return strconv.FormatUint(uint64(maxUsers), 10)
}

func channelTypeText(channelType domain.ChannelType) string {
	if channelType == domain.ChannelTypePermanent {
		return "permanent"
	}
	return strconv.FormatUint(uint64(channelType), 10)
}

func audioProfileText(profile domain.AudioProfile) string {
	codec := strconv.FormatUint(uint64(profile.Codec), 10)
	if profile.Codec == domain.AudioCodecOpus {
		codec = "opus"
	}
	application := strconv.FormatUint(uint64(profile.Application), 10)
	switch profile.Application {
	case domain.OpusApplicationAudio:
		application = "audio"
	case domain.OpusApplicationVoIP:
		application = "voip"
	}
	return fmt.Sprintf(
		"%s/%dHz/%dch/%dms/%dbps/%s",
		codec,
		profile.SampleRate,
		profile.Channels,
		profile.FrameDurationMS,
		profile.Bitrate,
		application,
	)
}

func udpAddressText(addr *net.UDPAddr) string {
	if addr == nil {
		return ""
	}
	return addr.String()
}

func nonNegativeDuration(duration time.Duration) time.Duration {
	if duration < 0 {
		return 0
	}
	return duration
}
