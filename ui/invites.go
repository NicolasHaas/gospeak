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

func inviteEntryText(invite pb.InviteInfo) string {
	uses := fmt.Sprintf("%d/%d", invite.UseCount, invite.MaxUses)
	if invite.MaxUses == 0 {
		uses = fmt.Sprintf("%d/unlimited", invite.UseCount)
	}
	validity := "expires never"
	if invite.ExpiresAt == 1 {
		validity = "revoked"
	} else if invite.ExpiresAt != 0 {
		validity = "expires " + time.Unix(invite.ExpiresAt, 0).Format("2006-01-02 15:04")
	}
	return fmt.Sprintf("#%d - %s - scope #%d - uses %s - %s\nCreated by #%d at %s", invite.ID, invite.Role, invite.ChannelScope, uses, validity, invite.CreatedBy, time.Unix(invite.CreatedAt, 0).Format("2006-01-02 15:04"))
}

func (a *App) showInviteListDialog() {
	attempt := a.connectAttempt
	closed, pending := false, false
	current := func() bool {
		return !closed && attempt == a.connectAttempt && a.engine.GetState() == client.StateConnected
	}
	var invites []pb.InviteInfo
	var selected, cursor int64
	var more bool
	status := widget.NewLabel("")
	list := widget.NewList(func() int { return len(invites) }, func() fyne.CanvasObject { return widget.NewLabel("Invite\nMetadata") }, func(id widget.ListItemID, object fyne.CanvasObject) {
		object.(*widget.Label).SetText(inviteEntryText(invites[id]))
	})
	list.OnSelected = func(id widget.ListItemID) { selected = invites[id].ID }
	list.OnUnselected = func(widget.ListItemID) { selected = 0 }
	var load func(int64)
	refresh := widget.NewButton("Refresh", func() { load(0) })
	next := widget.NewButton("Next page", func() {
		if more && len(invites) > 0 {
			load(invites[len(invites)-1].ID)
		}
	})
	revoke := widget.NewButton("Revoke selected", nil)
	setPending := func(value bool) {
		pending = value
		if value {
			refresh.Disable()
			next.Disable()
			revoke.Disable()
		} else {
			refresh.Enable()
			revoke.Enable()
			if more {
				next.Enable()
			} else {
				next.Disable()
			}
		}
	}
	load = func(after int64) {
		if !current() || pending {
			return
		}
		setPending(true)
		status.SetText("Loading invites...")
		err := a.engine.ListInvites(after, func(response pb.ListInvitesResponse) {
			fyne.Do(func() {
				if !current() {
					return
				}
				setPending(false)
				if response.Error != "" {
					status.SetText(response.Error)
					return
				}
				cursor, more, invites = after, response.HasMore, response.Invites
				list.UnselectAll()
				selected = 0
				list.Refresh()
				setPending(false)
				status.SetText(fmt.Sprintf("%d invites on this page", len(invites)))
			})
		})
		if err != nil {
			setPending(false)
			status.SetText(err.Error())
		}
	}
	revoke.OnTapped = func() {
		if !current() || pending || selected == 0 {
			return
		}
		id := selected
		setPending(true)
		dialog.ShowConfirm("Revoke invite", fmt.Sprintf("Revoke invite #%d? Existing accounts keep their personal credentials.", id), func(ok bool) {
			if !current() {
				return
			}
			if !ok {
				setPending(false)
				return
			}
			err := a.engine.RevokeInvite(id, func(response pb.RevokeInviteResponse) {
				fyne.Do(func() {
					if !current() {
						return
					}
					setPending(false)
					if response.Error != "" {
						status.SetText(response.Error)
						return
					}
					if !response.Revoked {
						dialog.ShowInformation("Revoke invite", "The invite is already revoked or no longer exists.", a.window)
					}
					load(cursor)
				})
			})
			if err != nil {
				setPending(false)
				status.SetText(err.Error())
			}
		}, a.window)
	}
	body := container.NewBorder(container.NewVBox(widget.NewLabel("Invite metadata only - expired and exhausted invites are included."), status), container.NewHBox(refresh, next, revoke), nil, nil, list)
	d := dialog.NewCustom("Manage Invites", "Close", body, a.window)
	d.SetOnClosed(func() { closed = true })
	d.Resize(fyne.NewSize(700, 420))
	d.Show()
	load(0)
}
