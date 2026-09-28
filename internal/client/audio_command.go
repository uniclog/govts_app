package client

import (
	"fmt"
	"io"
	"strconv"

	"uniclog.io/govts/internal/audio/voicegate"
)

func HandleAudioCommand(command Command, state *State, output io.Writer) bool {
	if command.Name == "/vad-mode" {
		if len(command.Arguments) != 1 {
			fmt.Fprintln(output, "usage: /vad-mode <level|vad|hybrid>")
			return true
		}
		mode, err := voicegate.ParseMode(command.Arguments[0])
		if err != nil || mode == voicegate.ModeDisabled || state.Audio.SetVADMode(mode) != nil {
			fmt.Fprintln(output, "vad-mode must be level, vad, or hybrid")
			return true
		}
		fmt.Fprintf(output, "vad-mode: %s\n", mode)
		return true
	}
	if command.Name == "/vad-sensitivity" {
		if len(command.Arguments) != 1 {
			fmt.Fprintln(output, "usage: /vad-sensitivity <0..1>")
			return true
		}
		value, err := strconv.ParseFloat(command.Arguments[0], 32)
		if err != nil || state.Audio.SetVADSensitivity(float32(value)) != nil {
			fmt.Fprintln(output, "vad-sensitivity must be a number between 0 and 1")
			return true
		}
		fmt.Fprintf(output, "vad-sensitivity: %.2f\n", value)
		return true
	}
	if command.Name != "/mute" && command.Name != "/deafen" && command.Name != "/rnnoise" && command.Name != "/vad" {
		return false
	}
	mode := "toggle"
	if len(command.Arguments) == 1 {
		mode = command.Arguments[0]
	}
	if len(command.Arguments) > 1 || (mode != "on" && mode != "off" && mode != "toggle") {
		fmt.Fprintf(output, "usage: %s [on|off|toggle]\n", command.Name)
		return true
	}
	muted, deafened, _ := state.Audio.Snapshot()
	value := muted
	if command.Name == "/deafen" {
		value = deafened
	} else if command.Name == "/rnnoise" {
		value = state.Audio.RNNoiseEnabled()
	} else if command.Name == "/vad" {
		value = state.Audio.VADSnapshot().Enabled
	}
	switch mode {
	case "on":
		value = true
	case "off":
		value = false
	case "toggle":
		value = !value
	}
	if command.Name == "/mute" {
		if err := state.Audio.SetMuted(value); err != nil {
			fmt.Fprintf(output, "mute failed: %v\n", err)
			return true
		}
	} else if command.Name == "/rnnoise" {
		state.Audio.SetRNNoiseEnabled(value)
	} else if command.Name == "/vad" {
		state.Audio.SetVADEnabled(value)
	} else if err := state.Audio.SetDeafened(value); err != nil {
		fmt.Fprintf(output, "deafen failed: %v\n", err)
		return true
	}
	fmt.Fprintf(output, "%s: %t\n", command.Name[1:], value)
	return true
}
