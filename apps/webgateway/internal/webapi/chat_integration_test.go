package webapi

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/pion/webrtc/v4"
	serverfiles "uniclog.io/sonoryx/apps/server/testsupport/filetransfer"
	servermedia "uniclog.io/sonoryx/apps/server/testsupport/media"
	"uniclog.io/sonoryx/apps/server/testsupport/persist"
	"uniclog.io/sonoryx/apps/server/testsupport/voice"
	"uniclog.io/sonoryx/apps/webgateway/internal/config"
	"uniclog.io/sonoryx/apps/webgateway/internal/session"
	"uniclog.io/sonoryx/apps/webgateway/internal/webrtcaudio"
	"uniclog.io/sonoryx/internal/appversion"
	"uniclog.io/sonoryx/internal/clientcore"
	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/fileapi"
	"uniclog.io/sonoryx/internal/identity"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
)

func TestChatBetweenWebSocketAndSonoryxClient(t *testing.T) {
	dir := t.TempDir()
	endpoint, publicKey, mediaPin := startChatUDP(t, dir)
	pins := filepath.Join(dir, "pins.json")
	if err := identity.CheckOrTrust(pins, endpoint, publicKey); err != nil {
		t.Fatal(err)
	}

	httpServer := httptest.NewUnstartedServer(nil)
	origin := "https://" + httpServer.Listener.Addr().String()
	cfg := config.Config{LoginMode: "nickname", Origin: origin, Endpoint: endpoint, StateDir: dir, MediaFingerprint: mediaPin,
		SessionTTL: time.Minute, ReconnectGrace: 10 * time.Second, MaxSessions: 4}
	manager := session.NewManager(cfg, pins, nil)
	t.Cleanup(manager.Close)
	httpServer.Config.Handler = New(cfg, manager)
	httpServer.StartTLS()
	t.Cleanup(httpServer.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	desktop := clientcore.New(clientcore.Options{Audio: webrtcaudio.NewBridge(),
		IdentityPath: filepath.Join(dir, "desktop.seed"), PinsPath: pins})
	t.Cleanup(func() { _ = desktop.Close(context.Background()) })
	if err := desktop.Connect(clientcore.ConnectOptions{Name: "Desktop", Server: endpoint}); err != nil {
		t.Fatal(err)
	}
	update, unsubscribe := desktop.Subscribe(ctx)
	defer unsubscribe()
	desktopView := desktop.Snapshot()
	for desktopView.ConnectionStatus != clientcore.ConnectionConnected || desktopView.UserID == 0 {
		select {
		case _, ok := <-update:
			if !ok {
				t.Fatal("desktop client stopped before entering a channel")
			}
			desktopView = desktop.Snapshot()
		case <-ctx.Done():
			t.Fatalf("desktop did not connect: %v", ctx.Err())
		}
	}
	channel := decimal(desktopView.ChannelID)
	desktopContext := desktop.ChatContext() + "|" + signed(desktopView.UserID)
	channelTarget := domain.ChatTarget{Kind: domain.ChatChannel, ID: int64(desktopView.ChannelID)}
	sendDesktop := func(target domain.ChatTarget, tag byte, text string) {
		t.Helper()
		if _, err := desktop.ChatRequest(ctx, desktopContext, protocol.ChatRequest{Operation: protocol.ChatSend,
			Target: target, ClientID: [16]byte{tag}, Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	sendDesktop(channelTarget, 1, "history before browser login")

	client := httpServer.Client()
	client.Jar, _ = cookiejar.New(nil)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/api/session", bytes.NewBufferString(`{"nickname":"Browser"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", origin)
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("login HTTP status = %d", response.StatusCode)
	}

	ws, _, err := websocket.Dial(ctx, "wss"+strings.TrimPrefix(origin, "https")+"/api/events", &websocket.DialOptions{
		HTTPClient: client, HTTPHeader: http.Header{"Origin": {origin}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	readView := func(revision string) snapshot {
		t.Helper()
		for {
			var view snapshot
			if err := wsjson.Read(ctx, ws, &view); err != nil {
				t.Fatal(err)
			}
			if view.Status == "connected" && view.ChannelID == channel && view.ChatRevision != revision {
				return view
			}
		}
	}
	browser := readView("")
	settingsRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/api/audio", strings.NewReader(`{"name":"gain","number":1.25}`))
	if err != nil {
		t.Fatal(err)
	}
	settingsRequest.Header.Set("Content-Type", "application/json")
	settingsRequest.Header.Set("Origin", origin)
	settingsResponse, err := client.Do(settingsRequest)
	if err != nil {
		t.Fatal(err)
	}
	_ = settingsResponse.Body.Close()
	if settingsResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("gain setting returned %d", settingsResponse.StatusCode)
	}
	adminRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/api/admin", strings.NewReader(`{"operation":"accounts"}`))
	if err != nil {
		t.Fatal(err)
	}
	adminRequest.Header.Set("Content-Type", "application/json")
	adminRequest.Header.Set("Origin", origin)
	adminResponse, err := client.Do(adminRequest)
	if err != nil {
		t.Fatal(err)
	}
	_ = adminResponse.Body.Close()
	if adminResponse.StatusCode != http.StatusForbidden {
		t.Fatalf("ordinary account reached admin: %d", adminResponse.StatusCode)
	}
	call := func(request command) chatPage {
		t.Helper()
		request.ID = request.Type[5:]
		if err := wsjson.Write(ctx, ws, request); err != nil {
			t.Fatal(err)
		}
		for {
			_, raw, err := ws.Read(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var reply result
			if err := json.Unmarshal(raw, &reply); err != nil {
				t.Fatal(err)
			}
			if reply.Type != "result" {
				continue
			}
			if reply.ID != request.ID || !reply.OK || reply.Page == nil {
				t.Fatalf("%s failed: %+v", request.Type, reply)
			}
			return *reply.Page
		}
	}
	history := call(command{Type: "chat.history", ChannelID: channel})
	if len(history.Messages) != 1 || history.Messages[0].Text != "history before browser login" {
		t.Fatalf("unexpected initial history: %+v", history)
	}

	sendDesktop(channelTarget, 2, "new desktop message")
	_ = readView(browser.ChatRevision)
	forward := true
	newer := call(command{Type: "chat.history", ChannelID: channel, Cursor: history.Cursor, Forward: &forward})
	if len(newer.Messages) != 1 || newer.Messages[0].Text != "new desktop message" || newer.Cursor == history.Cursor {
		t.Fatalf("unexpected forward history: %+v", newer)
	}
	empty := call(command{Type: "chat.history", ChannelID: channel, Cursor: newer.Cursor, Forward: &forward})
	if len(empty.Messages) != 0 || empty.Cursor != newer.Cursor {
		t.Fatalf("second catch-up returned duplicates or lost cursor: %+v", empty)
	}
	messageID := newer.Messages[0].ID
	changes := call(command{Type: "chat.changes", ChannelID: channel})
	if len(changes.Messages) != 0 {
		t.Fatalf("new messages unexpectedly appeared in the mutation log: %+v", changes)
	}
	reacted := call(command{Type: "chat.react", ChannelID: channel, Cursor: messageID, Text: "👍", Forward: &forward})
	if len(reacted.Messages) != 1 || len(reacted.Messages[0].Reactions) != 1 || !reacted.Messages[0].Reactions[0].Mine {
		t.Fatalf("reaction not returned: %+v", reacted)
	}
	edited := call(command{Type: "chat.changes", ChannelID: channel, Cursor: changes.Cursor})
	if len(edited.Messages) != 1 || edited.Messages[0].ID != messageID || edited.Cursor == changes.Cursor {
		t.Fatalf("reaction missing from the mutation log: %+v", edited)
	}
	call(command{Type: "chat.pin", ChannelID: channel, Cursor: messageID, Forward: &forward})
	pinsPage := call(command{Type: "chat.pins", ChannelID: channel, Forward: &forward})
	if len(pinsPage.PinnedIDs) != 1 || pinsPage.PinnedIDs[0] != messageID {
		t.Fatalf("pin not returned: %+v", pinsPage)
	}

	direct := call(command{Type: "chat.send", UserID: signed(desktopView.UserID), Text: "private browser message", ClientID: "03000000000000000000000000000000"})
	if len(direct.Messages) != 1 {
		t.Fatalf("unexpected direct send result: %+v", direct)
	}
	if !slices.ContainsFunc(call(command{Type: "chat.dialogs"}).Dialogs, func(dialog dialogView) bool {
		return dialog.UserID == signed(desktopView.UserID)
	}) {
		t.Fatal("browser did not see the new private conversation")
	}
	desktopDialogs, err := desktop.ChatRequest(ctx, desktopContext, protocol.ChatRequest{Operation: protocol.ChatDialogs})
	if err != nil || len(desktopDialogs.Dialogs) != 1 || signed(desktopDialogs.Dialogs[0].UserID) != browser.UserID {
		t.Fatalf("desktop did not see the web conversation: %+v, %v", desktopDialogs, err)
	}
	privateHistory, err := desktop.ChatRequest(ctx, desktopContext, protocol.ChatRequest{
		Operation: protocol.ChatHistory, Target: domain.ChatTarget{Kind: domain.ChatDirect, ID: desktopDialogs.Dialogs[0].UserID},
	})
	if err != nil || len(privateHistory.Messages) != 1 || privateHistory.Messages[0].Text != "private browser message" {
		t.Fatalf("desktop did not receive the private web message: %+v, %v", privateHistory, err)
	}

	content := []byte("web-to-Sonoryx file")
	attachmentID := uploadChatFixture(t, ctx, client, origin, channel, content)
	fileMessage := call(command{Type: "chat.send", ChannelID: channel, ClientID: "04000000000000000000000000000000", AttachmentIDs: []string{attachmentID}})
	if len(fileMessage.Messages) != 1 || !slices.Equal(fileMessage.Messages[0].Attachments, []string{attachmentID}) {
		t.Fatalf("attachment missing from the web chat result: %+v", fileMessage)
	}
	response, err = client.Get(origin + "/api/files/attachments/" + attachmentID)
	if err != nil {
		t.Fatal(err)
	}
	received, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK || !bytes.Equal(received, content) {
		t.Fatalf("attachment download status = %d, bytes = %q, err = %v", response.StatusCode, received, err)
	}
	checkScreenSignaling(t, ctx, client, origin)
}

func checkScreenSignaling(t *testing.T, ctx context.Context, client *http.Client, origin string) {
	t.Helper()
	response, err := client.Get(origin + "/api/media/capabilities")
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("media capabilities returned %d", response.StatusCode)
	}
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000}, "screen", "browser")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pc.AddTransceiverFromTrack(track, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly}); err != nil {
		t.Fatal(err)
	}
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gathered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	post := func(path string, input any, status int) []byte {
		t.Helper()
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", origin)
		req.Header.Set("Content-Type", "application/json")
		reply, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer reply.Body.Close()
		payload, err := io.ReadAll(reply.Body)
		if err != nil || reply.StatusCode != status {
			t.Fatalf("%s returned %d: %s (%v)", path, reply.StatusCode, payload, err)
		}
		return payload
	}
	payload := post("/api/media/publish", map[string]any{"offer": pc.LocalDescription()}, http.StatusOK)
	var published struct {
		StreamID string                    `json:"streamId"`
		Answer   webrtc.SessionDescription `json:"answer"`
	}
	if err := json.Unmarshal(payload, &published); err != nil || !validStreamID(published.StreamID) {
		t.Fatalf("invalid media result: %v", err)
	}
	if err := pc.SetRemoteDescription(published.Answer); err != nil {
		t.Fatal(err)
	}
	post("/api/media/stop", map[string]string{"streamId": published.StreamID}, http.StatusNoContent)
}

func uploadChatFixture(t *testing.T, ctx context.Context, client *http.Client, origin, channel string, content []byte) string {
	t.Helper()
	request := func(method, path, contentType string, body []byte) fileapi.FileUpload {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, origin+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", origin)
		req.Header.Set("Content-Type", contentType)
		if method == http.MethodPatch {
			req.Header.Set("Upload-Offset", "0")
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s %s returned %d", method, path, resp.StatusCode)
		}
		var state fileapi.FileUpload
		if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	digest := sha256.Sum256(content)
	body, err := json.Marshal(map[string]any{
		"requestId": "05000000000000000000000000000000", "channelId": channel,
		"name": "browser.txt", "size": len(content), "sha256": hex.EncodeToString(digest[:]),
	})
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/files/uploads"
	state := request(http.MethodPost, base, "application/json", body)
	path := base + "/" + state.ID
	state = request(http.MethodPatch, path, "application/octet-stream", content)
	if state.Offset != int64(len(content)) {
		t.Fatalf("invalid upload offset %d", state.Offset)
	}
	state = request(http.MethodPost, path+"/complete", "", nil)
	for state.State == "verifying" {
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(100 * time.Millisecond):
			state = request(http.MethodGet, path, "", nil)
		}
	}
	if state.State != "ready" || state.AttachmentID <= 0 {
		t.Fatalf("upload was not verified: %+v", state)
	}
	return signed(state.AttachmentID)
}

func startChatUDP(t *testing.T, dir string) (string, ed25519.PublicKey, string) {
	t.Helper()
	raw, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	codec := protocol.NewSecureDatagramCodec(true)
	conn, err := udp.NewServerPacketConn(raw, codec)
	if err != nil {
		t.Fatal(err)
	}
	store, err := persist.Open(filepath.Join(dir, "voice.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := voice.NewAuthenticator(privateKey, store, codec)
	if err != nil {
		t.Fatal(err)
	}
	hub := voice.NewHub()
	info := hub.Inspect().ServerInfo
	if err := store.SaveInitialServer(t.Context(), persist.ServerState{
		Name: info.Name, DefaultChannelID: info.DefaultChannelID, Channels: hub.ListChannels(),
	}); err != nil {
		t.Fatal(err)
	}
	version, err := appversion.Parse(appversion.ServerVersion)
	if err != nil {
		t.Fatal(err)
	}
	hub.SetServerVersion(version)
	ctx, cancel := context.WithCancel(context.Background())
	files, err := serverfiles.New(store, hub, filepath.Join(dir, "files"))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	fileServer := httptest.NewTLSServer(files)
	hub.SetFileEndpoint(uint16(fileServer.Listener.Addr().(*net.TCPAddr).Port), identity.TLSFingerprint(fileServer.Certificate()))
	mediaHandler, closeMedia, err := servermedia.New(hub)
	if err != nil {
		cancel()
		fileServer.Close()
		t.Fatal(err)
	}
	mediaServer := httptest.NewTLSServer(mediaHandler)
	hub.SetMediaPort(uint16(mediaServer.Listener.Addr().(*net.TCPAddr).Port))
	pin := identity.TLSFingerprint(mediaServer.Certificate())
	filesDone := make(chan struct{})
	go func() { defer close(filesDone); files.Run(ctx) }()
	done := make(chan error, 1)
	go func() { done <- voice.ServeUDP(ctx, conn, hub, voice.NewRequestCache(), auth) }()
	t.Cleanup(func() {
		cancel()
		fileServer.Close()
		mediaServer.Close()
		closeMedia()
		<-filesDone
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("UDP server stopped: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Error("UDP server did not stop")
		}
	})
	return raw.LocalAddr().String(), publicKey, pin
}
