package media

import (
	"bufio"
	"bytes"
	"errors"
	"io/fs"
	"os"
	"strconv"
	"strings"
)

// USER_HZ is 100 on every Linux architecture the server targets.
const clockTicks = 100

// Reads /proc for this process: CPU ticks, RSS, the UDP sockets it owns
// (matched by inode) on media ports, and host-wide UDP error counters.
func readHostSample(network *bufferedMediaNet) hostSample {
	var sample hostSample
	stat, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return sample
	}
	// Fields after the parenthesised command name; utime/stime are 14 and 15.
	fields := strings.Fields(string(stat[bytes.LastIndexByte(stat, ')')+1:]))
	if len(fields) < 13 {
		return sample
	}
	utime, _ := strconv.ParseUint(fields[11], 10, 64)
	stime, _ := strconv.ParseUint(fields[12], 10, 64)
	sample.cpuTicks = utime + stime
	sample.rssBytes = statusKiB("/proc/self/status", "VmRSS:") * 1024
	sample.udpInErrors, sample.udpRcvbufErr = udpErrors()
	sample.ok = true

	inodes, inodesOK := socketInodes()
	// Both tables must be read: a port missing from an unread table is not closed.
	tablesOK := true
	tracked := network.mediaPorts()
	open := make(map[uint16]bool, len(tracked))
	for _, path := range []string{"/proc/self/net/udp", "/proc/self/net/udp6"} {
		tablesOK = readUDPTable(path, func(port uint16, inode string, rxQueue int64, drops uint64) {
			if _, media := tracked[port]; !media || !inodes[inode] {
				return
			}
			open[port] = true
			sample.mediaSockets++
			sample.socketDrops += drops
			sample.rxQueueMax = max(sample.rxQueueMax, rxQueue)
		}) && tablesOK
	}
	closed := make(map[uint16]uint64)
	for port, opened := range tracked {
		if !open[port] {
			closed[port] = opened
		}
	}
	// A failed /proc read must not forget sockets that are still open.
	if inodesOK && tablesOK {
		network.forgetPorts(closed)
	}
	return sample
}

func statusKiB(path, key string) int64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(line, key); ok {
			fields := strings.Fields(rest)
			if len(fields) == 0 {
				return 0
			}
			value, _ := strconv.ParseInt(fields[0], 10, 64)
			return value
		}
	}
	return 0
}

func socketInodes() (map[string]bool, bool) {
	inodes := make(map[string]bool)
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return inodes, false
	}
	for _, entry := range entries {
		target, err := os.Readlink("/proc/self/fd/" + entry.Name())
		if inode, ok := strings.CutPrefix(target, "socket:["); err == nil && ok {
			inodes[strings.TrimSuffix(inode, "]")] = true
		}
	}
	return inodes, true
}

// A missing table (udp6 when IPv6 is disabled) has no sockets and counts as read.
// Columns: sl local rem st tx_queue:rx_queue tr:tm retrnsmt uid timeout inode ref pointer drops.
func readUDPTable(path string, visit func(port uint16, inode string, rxQueue int64, drops uint64)) bool {
	file, err := os.Open(path)
	if err != nil {
		return errors.Is(err, fs.ErrNotExist)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Scan() // header
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 13 {
			continue
		}
		local := fields[1]
		port, err := strconv.ParseUint(local[strings.LastIndexByte(local, ':')+1:], 16, 16)
		if err != nil {
			continue
		}
		queues := fields[4]
		rxQueue, _ := strconv.ParseInt(queues[strings.IndexByte(queues, ':')+1:], 16, 64)
		drops, _ := strconv.ParseUint(fields[len(fields)-1], 10, 64)
		visit(uint16(port), fields[9], rxQueue, drops)
	}
	return scanner.Err() == nil
}

// Host-wide Udp InErrors and RcvbufErrors from /proc/net/snmp.
func udpErrors() (inErrors, rcvbufErrors uint64) {
	data, err := os.ReadFile("/proc/net/snmp")
	if err != nil {
		return 0, 0
	}
	var header []string
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "Udp: ") {
			continue
		}
		if header == nil {
			header = strings.Fields(line)
			continue
		}
		values := strings.Fields(line)
		for i := 1; i < len(header) && i < len(values); i++ {
			value, _ := strconv.ParseUint(values[i], 10, 64)
			switch header[i] {
			case "InErrors":
				inErrors = value
			case "RcvbufErrors":
				rcvbufErrors = value
			}
		}
		return
	}
	return
}
