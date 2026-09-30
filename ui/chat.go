package ui

import (
	"fmt"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/NicolasHaas/gospeak/pkg/client"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func (a *App) resetChat() {
	a.chatChannelID = 0
	a.chatRows = nil
	a.chatDeleted = nil
	a.chatHasMore = false
	a.chatLoading = false
	a.chatBeforeID = 0
	a.chatBox.Objects = nil
	a.chatBox.Refresh()
	a.chatMore.Hide()
	a.updateChatContext()
}

func (a *App) selectChatChannel(channelID int64) {
	if a.engine.GetState() != client.StateConnected || channelID <= 0 {
		return
	}
	if channelID == a.chatChannelID {
		a.updateChatContext()
		return
	}
	// The history request selects this text channel on the server before reading it.
	if err := a.engine.LoadChatHistory(channelID, 0); err != nil {
		dialog.ShowError(err, a.window)
		return
	}
	a.resetChat()
	a.chatChannelID = channelID
	a.chatDeleted = make(map[int64]bool)
	a.chatLoading = true
	a.updateChatContext()
	a.renderChat()
}

func (a *App) loadEarlierChat() {
	if a.chatLoading || !a.chatHasMore || a.chatBeforeID == 0 {
		return
	}
	if err := a.engine.LoadChatHistory(a.chatChannelID, a.chatBeforeID); err != nil {
		dialog.ShowError(err, a.window)
		return
	}
	a.chatLoading = true
	a.chatMore.Disable()
}

func (a *App) addChatMessage(message pb.ChatMessage) {
	if message.ChannelID != a.chatChannelID || a.chatDeleted[message.ID] {
		return
	}
	for _, row := range a.chatRows {
		if message.ID > 0 && row.ID == message.ID {
			return
		}
	}
	index := len(a.chatRows)
	if message.ID > 0 {
		for i, row := range a.chatRows {
			if row.ID > message.ID || row.ID == 0 {
				index = i
				break
			}
		}
	}
	a.chatRows = append(a.chatRows, pb.ChatMessage{})
	copy(a.chatRows[index+1:], a.chatRows[index:])
	a.chatRows[index] = message
	// ponytail: keep 500 visible rows; virtualize if browsing deeper history becomes necessary.
	if len(a.chatRows) > 500 {
		a.chatRows = a.chatRows[len(a.chatRows)-500:]
	}
	a.renderChat()
	a.chatScroll.ScrollToBottom()
}

func (a *App) addChatHistory(response pb.ChatHistoryResponse) {
	if response.ChannelID != a.chatChannelID || !a.chatLoading {
		return
	}
	a.chatLoading = false
	a.chatHasMore = response.HasMore
	wasEmpty := len(a.chatRows) == 0
	if len(response.Messages) > 0 {
		a.chatBeforeID = response.Messages[len(response.Messages)-1].ID
	}
	seen := make(map[int64]bool, len(a.chatRows))
	for _, row := range a.chatRows {
		if row.ID > 0 {
			seen[row.ID] = true
		}
	}
	// Server pages are newest first; prepend older rows in chronological order.
	older := make([]pb.ChatMessage, 0, len(response.Messages))
	for i := len(response.Messages) - 1; i >= 0; i-- {
		message := response.Messages[i]
		if message.ID <= 0 || seen[message.ID] || a.chatDeleted[message.ID] {
			continue
		}
		older = append(older, message)
		seen[message.ID] = true
	}
	a.chatRows = append(older, a.chatRows...)
	if len(a.chatRows) > 500 {
		a.chatRows = a.chatRows[len(a.chatRows)-500:]
		a.chatHasMore = false
	}
	a.renderChat()
	if wasEmpty {
		a.chatScroll.ScrollToBottom()
	}
}

func (a *App) removeChatMessage(event pb.ChatDeleteEvent) {
	if event.ChannelID != a.chatChannelID {
		return
	}
	a.chatDeleted[event.MessageID] = true
	for i, row := range a.chatRows {
		if row.ID == event.MessageID {
			a.chatRows = append(a.chatRows[:i], a.chatRows[i+1:]...)
			a.renderChat()
			return
		}
	}
}

func (a *App) renderChat() {
	rows := make([]fyne.CanvasObject, 0, len(a.chatRows))
	if a.chatChannelID != 0 && len(a.chatRows) == 0 {
		text := "No messages yet."
		if a.chatLoading {
			text = "Loading messages..."
		}
		rows = append(rows, widget.NewLabel(text))
	}
	role := a.engine.GetRole()
	canDelete := role == "admin" || role == "moderator"
	for _, message := range a.chatRows {
		label := widget.NewLabel(fmt.Sprintf("[%s] %s: %s", time.Unix(message.Timestamp, 0).Format("15:04"), message.SenderName, message.Text))
		label.Wrapping = fyne.TextWrapWord
		if !canDelete || message.ID <= 0 {
			rows = append(rows, label)
			continue
		}
		channelID, messageID := message.ChannelID, message.ID
		button := widget.NewButton("Delete", func() {
			dialog.ShowConfirm("Delete message?", "Remove this message for everyone?", func(ok bool) {
				if !ok || a.chatChannelID != channelID {
					return
				}
				if err := a.engine.DeleteChatMessage(channelID, messageID); err != nil {
					dialog.ShowError(err, a.window)
				}
			}, a.window)
		})
		button.Importance = widget.LowImportance
		rows = append(rows, container.NewBorder(nil, nil, nil, button, label))
	}
	a.chatBox.Objects = rows
	a.chatBox.Refresh()
	if a.chatHasMore && len(a.chatRows) > 0 {
		a.chatMore.Show()
		if a.chatLoading {
			a.chatMore.Disable()
		} else {
			a.chatMore.Enable()
		}
	} else {
		a.chatMore.Hide()
	}
}
