package appversion

import (
	"fmt"
	"strconv"
	"strings"

	releaseversion "uniclog.io/sonoryx/version"
)

var release = mustReadRelease()

var (
	ClientVersion        = release.ClientVersion
	ServerVersion        = release.ServerVersion
	MinimumServerVersion = release.MinServerVersion
)

// VoiceBundlesMinimumVersion is the first handshake level supporting voice bundles.
// This protocol capability threshold must not track the current release minimum.
const VoiceBundlesMinimumVersion = "0.2.13"

var VoiceBundlesMinimum = mustParse(VoiceBundlesMinimumVersion)

func mustReadRelease() releaseversion.Release {
	config, err := releaseversion.Read()
	if err != nil {
		panic(fmt.Errorf("version/release.json: %w", err))
	}
	for _, field := range []struct{ name, value string }{
		{"clientVersion", config.ClientVersion},
		{"serverVersion", config.ServerVersion},
		{"minServerVersion", config.MinServerVersion},
	} {
		if _, err := Parse(field.value); err != nil {
			panic(fmt.Errorf("version/release.json %s: %w", field.name, err))
		}
	}
	minimum := mustParse(config.MinServerVersion)
	if minimum > mustParse(config.ServerVersion) {
		panic("version/release.json: minServerVersion exceeds serverVersion")
	}
	if minimum < mustParse(VoiceBundlesMinimumVersion) {
		panic("version/release.json: minServerVersion must support voice bundles")
	}
	return config
}

// Number fits in the existing 32-bit Sequence field of Hello packets.
// Components are encoded as major:8, minor:8, patch:16.
type Number uint32

func mustParse(text string) Number {
	number, err := Parse(text)
	if err != nil {
		panic(err)
	}
	return number
}

func Parse(text string) (Number, error) {
	parts := strings.Split(text, ".")
	if len(parts) != 3 {
		return 0, fmt.Errorf("invalid version %q: expected major.minor.patch", text)
	}
	var values [3]uint64
	limits := [3]uint64{255, 255, 65535}
	for i, part := range parts {
		value, err := strconv.ParseUint(part, 10, 64)
		if err != nil || strconv.FormatUint(value, 10) != part || value > limits[i] {
			return 0, fmt.Errorf("invalid version %q: components must be canonical non-negative integers within 255.255.65535", text)
		}
		values[i] = value
	}
	return Number(values[0]<<24 | values[1]<<16 | values[2]), nil
}

func (n Number) String() string {
	return fmt.Sprintf("%d.%d.%d", uint32(n)>>24, (uint32(n)>>16)&255, uint32(n)&65535)
}
