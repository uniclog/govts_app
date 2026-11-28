package media

import (
	"fmt"
	"log"
	"runtime"
	"strconv"
	"time"
)

// hostSample is read from the OS; ok is false where it is not available.
type hostSample struct {
	ok           bool
	cpuTicks     uint64 // process utime+stime, clock ticks
	rssBytes     int64
	mediaSockets int
	rxQueueMax   int64  // largest unread bytes on one media socket
	socketDrops  uint64 // cumulative drops of currently open media sockets
	udpInErrors  uint64 // host-wide, cumulative
	udpRcvbufErr uint64 // host-wide, cumulative
}

// Owned by the metrics worker. Deltas cover the interval since the previous
// sample; the first sample only sets the baseline.
type processSampler struct {
	at      time.Time
	host    hostSample
	numGC   uint32
	pauseNs uint64
}

func (m *Manager) logProcessMetrics(active bool) {
	now := time.Now()
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	host := readHostSample(m.network)
	s := &m.process
	previous, hasBaseline := *s, !s.at.IsZero()
	*s = processSampler{at: now, host: host, numGC: mem.NumGC, pauseNs: mem.PauseTotalNs}
	if !active && host.mediaSockets == 0 {
		return
	}
	na := "na"
	cpu, gcCycles, gcPause, inErrors, rcvbufErrors := na, na, na, na, na
	if hasBaseline {
		elapsed := now.Sub(previous.at).Seconds()
		gcCycles = strconv.FormatUint(uint64(mem.NumGC-previous.numGC), 10)
		gcPause = fmt.Sprintf("%.1f", float64(mem.PauseTotalNs-previous.pauseNs)/1e6)
		if host.ok && previous.host.ok && elapsed > 0 {
			cpu = fmt.Sprintf("%.1f", float64(host.cpuTicks-previous.host.cpuTicks)/clockTicks/elapsed*100)
			inErrors = strconv.FormatUint(host.udpInErrors-previous.host.udpInErrors, 10)
			rcvbufErrors = strconv.FormatUint(host.udpRcvbufErr-previous.host.udpRcvbufErr, 10)
		}
	}
	rss, sockets, rxQueue, drops := na, na, na, na
	if host.ok {
		rss = fmt.Sprintf("%.1f", float64(host.rssBytes)/(1<<20))
		sockets = strconv.Itoa(host.mediaSockets)
		rxQueue = strconv.FormatInt(host.rxQueueMax, 10)
		drops = strconv.FormatUint(host.socketDrops, 10)
	}
	rcvbuf := na
	if value := m.network.observedReadBuffer.Load(); value > 0 {
		rcvbuf = strconv.FormatInt(value, 10)
	}
	log.Printf("screen process: cpu_percent=%s rss_mib=%s heap_mib=%.1f goroutines=%d gc_cycles=%s gc_pause_ms=%s media_sockets=%s socket_rx_queue_max_bytes=%s socket_drops=%s effective_rcvbuf=%s host_udp_in_errors=%s host_udp_rcvbuf_errors=%s",
		cpu, rss, float64(mem.HeapAlloc)/(1<<20), runtime.NumGoroutine(), gcCycles, gcPause, sockets, rxQueue, drops, rcvbuf, inErrors, rcvbufErrors)
}
