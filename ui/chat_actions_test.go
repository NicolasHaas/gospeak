package ui

import (
	"crypto/tls"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"github.com/NicolasHaas/gospeak/pkg/client"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestChatBottomButton(t *testing.T) {
	f := test.NewApp()
	defer f.Quit()
	a := &App{fyneApp: f, window: f.NewWindow("GoSpeak"), engine: client.NewEngine()}
	a.buildUI()
	var bottom *widget.Button
	var findButton func(fyne.CanvasObject)
	findButton = func(obj fyne.CanvasObject) {
		if button, ok := obj.(*widget.Button); ok && button.Text == "Scroll to bottom" {
			bottom = button
		}
		if box, ok := obj.(*fyne.Container); ok {
			for _, child := range box.Objects {
				findButton(child)
			}
		}
	}
	findButton(a.chatPane)
	if bottom == nil {
		t.Fatal("missing labeled chat bottom button")
	}
	check := func(state string, visible bool) {
		t.Helper()
		if bottom.Visible() != visible {
			t.Fatalf("%s: button visible %v, want %v (content %v, viewport %v, offset %v)",
				state, bottom.Visible(), visible, a.chatBox.MinSize(), a.chatScroll.Size(), a.chatScroll.Offset)
		}
	}
	a.chatPane.Resize(fyne.NewSize(400, 200))
	check("empty", false)
	a.chatChannelID = 1
	a.chatDeleted = make(map[int64]bool)
	a.addChatMessage(pb.ChatMessage{ID: 1, ChannelID: 1, Text: "message"})
	check("short chat", false)
	for i := int64(2); i <= 40; i++ {
		a.chatRows = append(a.chatRows, pb.ChatMessage{ID: i, ChannelID: 1, Text: "message"})
	}
	a.renderChat()
	a.chatScroll.ScrollToBottom()
	check("overflow at bottom", false)
	a.chatScroll.Scrolled(&fyne.ScrollEvent{Scrolled: fyne.Delta{DY: 50}})
	check("wheel up", true)
	test.Tap(bottom)
	want := a.chatBox.MinSize().Height - a.chatScroll.Size().Height
	if want <= 0 || a.chatScroll.Offset.Y != want || len(a.chatRows) != 40 {
		t.Fatalf("bottom action: offset %v, want %v; rows %d", a.chatScroll.Offset.Y, want, len(a.chatRows))
	}
	check("tapped bottom", false)
	a.chatScroll.Scrolled(&fyne.ScrollEvent{Scrolled: fyne.Delta{DY: 100000}})
	check("top", true)
	a.chatScroll.Scrolled(&fyne.ScrollEvent{Scrolled: fyne.Delta{DY: -50}})
	a.chatPane.Resize(fyne.NewSize(400, a.chatBox.MinSize().Height-a.chatScroll.Offset.Y))
	check("resize reaches bottom without moving offset or content", false)
	a.chatPane.Resize(fyne.NewSize(400, 200))
	check("resize leaves bottom without moving offset or content", true)
	a.chatPane.Resize(fyne.NewSize(400, a.chatBox.MinSize().Height+1))
	check("grown to fit", false)
	a.chatPane.Resize(fyne.NewSize(400, 200))
	check("shrunk from fitting", true)
	a.chatScroll.Scrolled(&fyne.ScrollEvent{Scrolled: fyne.Delta{DY: -100000}})
	check("wheel down", false)
	a.chatScroll.Scrolled(&fyne.ScrollEvent{Scrolled: fyne.Delta{DY: 100000}})
	a.addChatMessage(pb.ChatMessage{ID: 41, ChannelID: 1, Text: "new message"})
	check("incoming message keeps existing autoscroll", false)
	a.chatScroll.Scrolled(&fyne.ScrollEvent{Scrolled: fyne.Delta{DY: 100000}})
	for i := int64(2); i <= 41; i++ {
		a.removeChatMessage(pb.ChatDeleteEvent{ChannelID: 1, MessageID: i})
	}
	check("deleted down to fitting", false)

	// One wrapped row can overflow; width changes must update without a scroll event.
	a.chatRows[0].Text = strings.Repeat("wrapped message ", 30)
	a.renderChat()
	a.chatPane.Resize(fyne.NewSize(200, 200))
	check("narrow wrapped row", true)
	a.chatPane.Resize(fyne.NewSize(2000, 200))
	check("wide wrapped row fits", false)
	a.chatPane.Resize(fyne.NewSize(400, 200))
	a.chatRows[0].Text = "message"
	a.renderChat()
	a.chatPane.Resize(fyne.NewSize(400, a.chatBox.MinSize().Height+1))
	check("near fit without button stealing height", false)
	a.chatHasMore = true
	a.renderChat()
	check("earlier messages bar reduces viewport", true)
	a.chatHasMore = false
	a.renderChat()
	check("earlier messages bar removed", false)
	a.chatPane.Resize(fyne.NewSize(200, 100))
	a.chatRows[0].Text = strings.Repeat("wrapped message ", 30)
	a.renderChat()
	check("overflow before reset", true)
	a.resetChat()
	check("reset", false)
	a.chatChannelID = 2
	a.chatLoading = true
	a.addChatHistory(pb.ChatHistoryResponse{ChannelID: 2, HasMore: true, Messages: []pb.ChatMessage{
		{ID: 2, ChannelID: 2, Text: strings.Repeat("history message ", 30)},
	}})
	check("initial history at bottom", false)
	a.chatScroll.Scrolled(&fyne.ScrollEvent{Scrolled: fyne.Delta{DY: 100000}})
	check("history scrolled up", true)
	a.chatLoading = true
	a.addChatHistory(pb.ChatHistoryResponse{ChannelID: 2, Messages: []pb.ChatMessage{
		{ID: 1, ChannelID: 2, Text: "earlier message"},
	}})
	check("earlier history leaves action visible", true)
	a.bindEvents()
	a.engine.OnStateChange(client.StateDisconnected)
	fyne.DoAndWait(func() {})
	check("disconnect", false)
}

func TestChannelJoinButtonUsesRecycledRow(t *testing.T) {
	f := test.NewApp()
	defer f.Quit()
	a := &App{fyneApp: f, window: f.NewWindow("GoSpeak"), engine: client.NewEngine()}
	a.buildUI()
	a.channels = []pb.ChannelInfo{{ID: 1, Name: "Lobby", Users: []pb.UserInfo{{Username: "Guest"}}}, {ID: 2, Name: "Games"}}
	a.selectedChannelID = 1
	row := a.channelList.CreateItem().(*fyne.Container)
	var join *widget.Button
	for _, obj := range row.Objects {
		if button, ok := obj.(*widget.Button); ok && button.Text == "Join" {
			join = button
		}
	}
	if join == nil {
		t.Fatal("missing labeled channel join icon")
	}
	a.updateChannelListItem(0, row)
	if !join.Disabled() {
		t.Fatal("disconnected row permits join")
	}

	// A tiny TLS control peer observes the real Engine.JoinChannel wire request.
	certServer := httptest.NewTLSServer(nil)
	defer certServer.Close()
	listener, err := tls.Listen("tcp", "127.0.0.1:0", certServer.TLS)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	requests := make(chan int64, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		if _, err = protocol.ReadControlMessage(conn); err != nil {
			return
		}
		if err = protocol.WriteControlMessage(conn, &pb.ControlMessage{AuthResponse: &pb.AuthResponse{
			SessionID: 1, Username: "Guest", Role: "user", MediaCipher: "aes128",
			EncryptionKey: make([]byte, 16), VoiceRegistrationKey: make([]byte, 32),
		}}); err != nil {
			return
		}
		for {
			msg, err := protocol.ReadControlMessage(conn)
			if err != nil {
				return
			}
			if msg.JoinChannelRequest != nil {
				id := msg.JoinChannelRequest.ChannelID
				requests <- id
				if err = protocol.WriteControlMessage(conn, &pb.ControlMessage{ChannelJoinResponse: &pb.ChannelJoinResponse{ChannelID: id, Success: true}}); err != nil {
					return
				}
			}
		}
	}()
	if err := a.engine.Connect(listener.Addr().String(), "127.0.0.1:9601", "", "Guest", client.SPKIFingerprint(certServer.Certificate())); err != nil {
		t.Fatal(err)
	}
	defer func() { a.engine.Disconnect(); <-done }()
	joined := make(chan int64, 4)
	a.engine.OnChannelJoined = func(id int64) { joined <- id }
	a.updateChannelListItem(0, row)
	if join.Disabled() {
		t.Fatal("connected channel cannot be joined")
	}
	a.updateChannelListItem(1, row)
	if join.Visible() || join.OnTapped != nil {
		t.Fatal("recycled user row retained channel action")
	}
	a.updateChannelListItem(2, row)
	test.Tap(join)
	select {
	case id := <-requests:
		if id != 2 || a.selectedChannelID != 1 {
			t.Fatalf("join targeted %d, selected text channel %d", id, a.selectedChannelID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("join did not reach control peer")
	}
	select {
	case <-joined:
	case <-time.After(2 * time.Second):
		t.Fatal("join acknowledgment did not arrive")
	}
	a.updateChannelListItem(2, row)
	if !join.Disabled() {
		t.Fatal("current voice channel permits redundant join")
	}
	join.OnTapped() // Shared handler must guard stale enabled callbacks too.
	a.joinSelectedChannel()
	select {
	case id := <-requests:
		if id != 1 {
			t.Fatalf("current membership sent a duplicate join or main Join Voice targeted %d", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("main Join Voice did not reach control peer")
	}
	a.engine.Disconnect()
	a.updateChannelListItem(0, row)
	if !join.Disabled() {
		t.Fatal("disconnect left join enabled")
	}
}
