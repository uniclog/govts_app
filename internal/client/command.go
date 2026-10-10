package client

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"strings"

	"uniclog.io/sonoryx/internal/transport/udp"
)

type Command struct {
	Name      string
	Arguments []string
}

func ReadCommandLoop(ctx context.Context, input io.Reader, commands chan<- Command) {
	if input == nil {
		return
	}
	scanner := bufio.NewScanner(input)
	for scanner.Scan() {
		parts := strings.Fields(scanner.Text())
		if len(parts) == 0 {
			continue
		}
		command := Command{Name: strings.ToLower(parts[0]), Arguments: append([]string(nil), parts[1:]...)}
		select {
		case commands <- command:
		case <-ctx.Done():
			return
		}
	}
}

func HandleOfflineCommand(command Command, output io.Writer, cancel context.CancelFunc, states ...*State) {
	if len(states) > 0 && HandleAudioCommand(command, states[0], output) {
		return
	}
	switch command.Name {
	case "/quit", "/q":
		cancel()
	case "/help":
		_ = WriteClientHelp(output)
	default:
		log.Printf("command %s unavailable while reconnecting", command.Name)
	}
}

func SessionCommandLoop(ctx context.Context, conn *udp.ClientPacketConn, state *State, commands <-chan Command, output io.Writer, cancelApp context.CancelFunc, onLocator func(ChannelLocator)) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case command, ok := <-commands:
			if !ok {
				commands = nil
				continue
			}
			if state != nil && HandleAudioCommand(command, state, output) {
				continue
			}
			switch command.Name {
			case "/join":
				handleJoin(ctx, conn, state, command.Arguments, output, onLocator)
			case "/channels":
				handleChannels(ctx, conn, state, output)
			case "/help":
				if err := WriteClientHelp(output); err != nil {
					return err
				}
			case "/quit", "/q":
				cancelApp()
				return nil
			default:
				log.Printf("unknown command: %s", command.Name)
			}
		}
	}
}

func WriteClientHelp(output io.Writer) error {
	if output == nil {
		return fmt.Errorf("command output is required")
	}
	_, err := io.WriteString(output, "commands:\n  /channels              refresh and show channels\n  /join <id|name>        join a channel\n  /mute [on|off|toggle]\n  /deafen [on|off|toggle]\n  /rnnoise [on|off|toggle]\n  /vad [on|off|toggle]\n  /vad-mode <level|vad|hybrid>\n  /vad-sensitivity <0..1>\n  /help                  show this help\n  /quit                  disconnect and exit\n")
	return err
}

func handleJoin(ctx context.Context, conn *udp.ClientPacketConn, state *State, parts []string, output io.Writer, onLocator func(ChannelLocator)) {
	if len(parts) != 1 {
		log.Printf("usage: /join <id|name>")
		return
	}
	selector := parts[0]
	channelID, err := ResolveChannel(state.Snapshot(), selector)
	if err != nil {
		log.Printf("join channel: %v", err)
		return
	}
	if err := JoinChannel(ctx, conn, state, channelID); err != nil {
		log.Printf("join channel: %v", err)
		return
	}
	snapshot, err := LoadServerSnapshot(ctx, conn, state)
	if err != nil {
		log.Printf("refresh server state after join: %v", err)
		return
	}
	locator, err := BuildChannelLocator(snapshot, channelID)
	if err != nil {
		log.Printf("remember joined channel: %v", err)
		return
	}
	if onLocator != nil {
		onLocator(locator)
	}
	if err := RenderChannelMembers(output, snapshot, channelID, state.SessionID()); err != nil {
		log.Printf("render joined channel: %v", err)
	}
}

func handleChannels(ctx context.Context, conn *udp.ClientPacketConn, state *State, output io.Writer) {
	snapshot, err := LoadServerSnapshot(ctx, conn, state)
	if err != nil {
		log.Printf("refresh channels: %v; showing stale snapshot", err)
		snapshot = state.Snapshot()
		_ = RenderServerTree(output, snapshot, state.SessionID(), state.ChannelID(), true)
		return
	}
	if err := RenderServerTree(output, snapshot, state.SessionID(), state.ChannelID(), false); err != nil {
		log.Printf("render channels: %v", err)
	}
}
