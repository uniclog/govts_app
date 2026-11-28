package webapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/time/rate"
	"uniclog.io/sonoryx/apps/webgateway/internal/session"
)

var requestID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func (h *handler) events(w http.ResponseWriter, r *http.Request) {
	if !h.origin(w, r) {
		return
	}
	s := h.require(w, r)
	if s == nil {
		return
	}
	detach, err := s.Attach()
	if err != nil {
		if err == session.ErrSocketActive {
			ws, acceptErr := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
			if acceptErr == nil {
				_ = ws.Close(4409, "tab already connected")
			}
			return
		}
		fail(w, http.StatusConflict, err.Error())
		return
	}
	defer detach()

	// Origin already passed the exact full-origin policy, including scheme and port.
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		return
	}
	ws.SetReadLimit(8192)
	ctx, cancel := context.WithCancel(s.Context())
	defer cancel()
	updates, unsubscribe := s.Core.Subscribe(ctx)
	defer unsubscribe()
	events, unwatchEvents := s.Core.SubscribeEvents(ctx)
	defer unwatchEvents()
	results := make(chan result, 8)
	commands := make(chan command, 8)
	readDone := make(chan struct{})
	executeDone := make(chan struct{})
	go func() {
		defer close(readDone)
		defer cancel()
		limiter := rate.NewLimiter(12, 16)
		for {
			kind, data, err := ws.Read(ctx)
			if err != nil {
				return
			}
			if kind != websocket.MessageText || !limiter.Allow() {
				return
			}
			var c command
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&c); err != nil || decoder.Decode(new(any)) != io.EOF || !requestID.MatchString(c.ID) {
				return
			}
			select {
			case commands <- c:
			default:
				return
			}
		}
	}()
	go func() {
		defer close(executeDone)
		for {
			var c command
			select {
			case <-ctx.Done():
				return
			case c = <-commands:
			}
			if ctx.Err() != nil {
				return
			}
			opCtx, stop := context.WithTimeout(ctx, 8*time.Second)
			page, err := execute(opCtx, s, c)
			stop()
			reply := result{Type: "result", ID: c.ID, OK: err == nil, Page: page}
			if err != nil {
				reply.Error = commandError(c.Type, err)
			}
			select {
			case <-ctx.Done():
				return
			case results <- reply:
			}
		}
	}()
	defer func() { cancel(); ws.CloseNow(); <-readDone; <-executeDone }()
	write := func(data []byte) error {
		writeCtx, stop := context.WithTimeout(ctx, 4*time.Second)
		defer stop()
		return ws.Write(writeCtx, websocket.MessageText, data)
	}
	initialView := state(s, "snapshot")
	initial, err := json.Marshal(initialView)
	if err != nil || write(initial) != nil {
		return
	}
	timer := time.NewTicker(125 * time.Millisecond)
	defer timer.Stop()
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()
	dirty := false
	var eventCursor uint64
	initialView.Type = "state"
	last, err := json.Marshal(initialView)
	if err != nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-updates:
			if !ok {
				return
			}
			dirty = true
		case _, ok := <-events:
			if !ok {
				return
			}
			entries := s.Core.EventsAfter(eventCursor)
			if len(entries) == 0 {
				continue
			}
			batch := eventBatch{Type: "events", Events: make([]eventView, 0, len(entries))}
			for _, entry := range entries {
				batch.Events = append(batch.Events, eventView{Sequence: decimal(entry.Sequence), Time: entry.Time, Kind: entry.Kind, Message: entry.Message})
			}
			encoded, err := json.Marshal(batch)
			if err != nil || write(encoded) != nil {
				return
			}
			eventCursor = entries[len(entries)-1].Sequence
		case reply := <-results:
			encoded, err := json.Marshal(reply)
			if err != nil || write(encoded) != nil {
				return
			}
		case <-timer.C:
			if !dirty {
				continue
			}
			dirty = false
			encoded, err := json.Marshal(state(s, "state"))
			if err != nil {
				return
			}
			if bytes.Equal(encoded, last) {
				continue
			}
			if write(encoded) != nil {
				return
			}
			last = encoded
		case <-heartbeat.C:
			pingCtx, stop := context.WithTimeout(ctx, 5*time.Second)
			err := ws.Ping(pingCtx)
			stop()
			if err != nil {
				return
			}
			dirty = true
		}
	}
}

type eventBatch struct {
	Type   string      `json:"type"`
	Events []eventView `json:"events"`
}

type eventView struct {
	Sequence string    `json:"sequence"`
	Time     time.Time `json:"time"`
	Kind     string    `json:"kind"`
	Message  string    `json:"message"`
}
