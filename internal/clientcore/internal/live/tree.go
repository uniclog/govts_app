package client

import (
	"sort"
	"strconv"
	"strings"

	"uniclog.io/sonoryx/internal/domain"
)

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

// Escape untrusted text even when a snapshot comes from an older server.
func terminalText(value string) string {
	quoted := strconv.QuoteToGraphic(value)
	return quoted[1 : len(quoted)-1]
}
