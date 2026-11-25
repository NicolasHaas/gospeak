package ui

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
	"github.com/NicolasHaas/gospeak/pkg/client"
	"github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func (a *App) showPersonalCredentialDialog() {
	address, username := widget.NewEntry(), widget.NewEntry()
	address.SetText(a.connectServer)
	username.SetText(a.engine.GetUsername())
	old, grant, account := widget.NewPasswordEntry(), widget.NewPasswordEntry(), widget.NewEntry()
	old.SetText(a.connectToken)
	consent := widget.NewCheck("Allow private bookmarking of this account if unsaved", nil)
	status := widget.NewLabel("Replacement is saved privately before any server change. All sessions retire; reconnect afterwards. Keep pending state until verified.")
	status.Wrapping = fyne.TextWrapWord
	pending, err := a.bookmarks.PersonalCredentialAttempts()
	if err != nil {
		dialog.ShowError(err, a.window)
		return
	}
	labels := make([]string, len(pending))
	for i, p := range pending {
		labels[i] = p.ControlAddr + " - " + p.Username
	}
	selected := widget.NewSelect(labels, func(label string) {
		for i, value := range labels {
			if value == label {
				address.SetText(pending[i].ControlAddr)
				username.SetText(pending[i].Username)
				account.SetText(strconv.FormatInt(pending[i].AccountID, 10))
				old.SetText(pending[i].OldToken)
				break
			}
		}
	})
	if len(labels) > 0 {
		selected.SetSelected(labels[0])
	}
	findPending := func() (client.PendingPersonalCredential, error) {
		attempts, err := a.bookmarks.PersonalCredentialAttempts()
		if err != nil {
			return client.PendingPersonalCredential{}, err
		}
		addr, _, err := normalizeAddr(address.Text, defaultControlPort)
		if err != nil {
			return client.PendingPersonalCredential{}, err
		}
		for _, p := range attempts {
			if p.ControlAddr == addr && p.Username == strings.TrimSpace(username.Text) {
				return p, nil
			}
		}
		return client.PendingPersonalCredential{}, fmt.Errorf("no saved attempt for this server/account")
	}
	var runPending func(client.PendingPersonalCredential, bool)
	runPending = func(p client.PendingPersonalCredential, retry bool) {
		if a.credentialBusy {
			return
		}
		if a.confirmCredentialDisconnect(func() { runPending(p, retry) }) {
			return
		}
		// A previous Save may have failed after rename. Re-establish successful
		// private persistence before an explicit retry can send any mutation.
		if retry {
			if err := a.bookmarks.StagePersonalCredential(p, false); err != nil {
				_ = a.bookmarks.ReloadPersonalCredentialState()
				status.SetText("Private staging failed; no retry sent. Retain the saved candidate.")
				return
			}
		}
		a.credentialBusy = true
		connectionAttempt := a.connectAttempt
		store := *a.bookmarks // immutable path/load state for this background request
		status.SetText("Checking the saved candidate...")
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			err := client.VerifyPersonalCredential(ctx, p)
			if client.PersonalCredentialRejected(err) && retry {
				// Explicit retry uses only the original saved operation. Successful old
				// authentication plus the original CAS, not an error classification, gates it.
				_ = store.SubmitPersonalCredential(ctx, p)
				err = client.VerifyPersonalCredential(ctx, p)
			}
			fyne.Do(func() {
				a.credentialBusy = false
				if err != nil {
					if a.confirmPendingCredentialTrust(err, p, status) {
						return
					}
					status.SetText("Outcome not verified. Saved candidate retained. Check trust/network, check again, or explicitly retry the saved operation.")
					return
				}
				if err := a.bookmarks.PromotePersonalCredential(p); err != nil {
					_ = a.bookmarks.ReloadPersonalCredentialState()
					status.SetText("Candidate authenticated, but private finalization was uncertain. Saved state reloaded where possible; retain the candidate and check again.")
					return
				}
				if connectionAttempt == a.connectAttempt && a.connectServer == p.ControlAddr && a.engine.GetUsername() == p.Username && a.connectToken == p.OldToken {
					a.connectToken = p.Candidate
				}
				status.SetText("Credential verified and privately saved. Reconnect using the saved bookmark.")
			})
		}()
	}
	var start func(bool)
	start = func(recovery bool) {
		if a.credentialBusy {
			return
		}
		if a.confirmCredentialDisconnect(func() { start(recovery) }) {
			return
		}
		addr, _, err := normalizeAddr(address.Text, defaultControlPort)
		if err != nil {
			dialog.ShowError(err, a.window)
			return
		}
		name, oldToken, bearer := strings.TrimSpace(username.Text), old.Text, grant.Text
		if name == "" {
			dialog.ShowError(fmt.Errorf("username is required"), a.window)
			return
		}
		attempts, err := a.bookmarks.PersonalCredentialAttempts()
		if err != nil {
			status.SetText(err.Error())
			return
		}
		var existing *client.PendingPersonalCredential
		for _, p := range attempts {
			if p.ControlAddr == addr && p.Username == name {
				if !recovery {
					status.SetText("An attempt is already saved. Check or retry it, use a fresh recovery grant, or verify an operator-recovered credential.")
					return
				}
				copy := p
				existing = &copy
			}
		}
		if !recovery {
			bearer = ""
		}
		if recovery && oldToken == "" {
			for _, b := range a.bookmarks.Bookmarks {
				if b.ControlAddr == addr && b.Username == name {
					oldToken = b.Token
					break
				}
			}
		}
		id, _ := strconv.ParseInt(strings.TrimSpace(account.Text), 10, 64)
		if recovery && (id <= 0 || !crypto.IsGeneratedToken(bearer)) {
			dialog.ShowError(fmt.Errorf("existing account ID and temporary recovery grant are required"), a.window)
			return
		}
		allowUnsaved, pin := consent.Checked, a.bookmarks.PinForAddr(addr)
		if existing != nil {
			if id != existing.AccountID {
				status.SetText("Saved attempt account mismatch; no change sent.")
				return
			}
			pin = existing.ServerIdentity
		}
		a.credentialBusy = true
		status.SetText("Verifying server and account before private staging...")
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			p := client.PendingPersonalCredential{ControlAddr: addr, Username: name, AccountID: id, OldToken: oldToken, RecoveryGrant: bearer}
			var preflightErr error
			candidateCurrent := false
			if existing != nil {
				preflightErr = client.VerifyPersonalCredential(ctx, *existing)
				candidateCurrent = preflightErr == nil
				if client.PersonalCredentialRejected(preflightErr) {
					preflightErr = nil
				}
				p.ServerIdentity = existing.ServerIdentity
			} else if recovery {
				c, err := client.NewControlClientContext(ctx, addr, pin)
				preflightErr = err
				if err == nil {
					p.ServerIdentity = c.VerifiedServerIdentity()
					_ = c.Close()
				}
			} else {
				c, auth, err := client.OpenPersonalTokenControl(ctx, addr, pin, name, oldToken)
				preflightErr = err
				if err == nil {
					p.AccountID, p.Version, p.ServerIdentity = auth.UserID, auth.CredentialVersion, c.VerifiedServerIdentity()
					_ = c.Close()
				}
			}
			fyne.Do(func() {
				a.credentialBusy = false
				if preflightErr != nil {
					if existing != nil && a.confirmPendingCredentialTrust(preflightErr, *existing, status) {
						return
					}
					status.SetText(preflightErr.Error())
					return
				}
				if existing != nil {
					if candidateCurrent {
						runPending(*existing, false)
						return
					}
					p, err = a.bookmarks.RecoverPendingPersonalCredential(*existing, bearer, p.ServerIdentity)
				} else {
					p.Candidate, err = crypto.GenerateToken()
					if err == nil {
						p.Attempt, err = crypto.GenerateToken()
					}
					if err == nil {
						err = a.bookmarks.StagePersonalCredential(p, allowUnsaved)
					}
				}
				if err != nil {
					_ = a.bookmarks.ReloadPersonalCredentialState()
					status.SetText("Private staging failed; no server change sent. Reload saved attempts before retrying: " + err.Error())
					return
				}
				runPending(p, true)
			})
		}()
	}
	rotate := widget.NewButton("Rotate my credential", func() { start(false) })
	redeem := widget.NewButton("Redeem recovery grant", func() { start(true) })
	check := widget.NewButton("Check saved candidate", func() {
		p, err := findPending()
		if err != nil {
			status.SetText(err.Error())
			return
		}
		runPending(p, false)
	})
	retry := widget.NewButton("Retry saved operation", func() {
		p, err := findPending()
		if err != nil {
			status.SetText(err.Error())
			return
		}
		runPending(p, true)
	})
	var verifyRecovered func()
	verifyRecovered = func() {
		if a.credentialBusy {
			return
		}
		if a.confirmCredentialDisconnect(verifyRecovered) {
			return
		}
		p, err := findPending()
		if err != nil {
			status.SetText(err.Error())
			return
		}
		token := old.Text
		if token == "" {
			status.SetText("Enter the privately retained operator-recovered credential.")
			return
		}
		a.credentialBusy = true
		connectionAttempt := a.connectAttempt
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			c, auth, err := client.OpenPersonalTokenControl(ctx, p.ControlAddr, p.ServerIdentity, p.Username, token)
			if err == nil {
				_ = c.Close()
				if auth.UserID != p.AccountID {
					err = fmt.Errorf("recovered credential account mismatch")
				}
			}
			fyne.Do(func() {
				a.credentialBusy = false
				if err != nil {
					if a.confirmPendingCredentialTrust(err, p, status) {
						return
					}
					status.SetText("Credential not verified; pending candidate retained.")
					return
				}
				if err := a.bookmarks.PromoteRecoveredPersonalCredential(p, token); err != nil {
					_ = a.bookmarks.ReloadPersonalCredentialState()
					status.SetText("Private finalization uncertain; reload saved state and verify again.")
					return
				}
				if connectionAttempt == a.connectAttempt && a.connectServer == p.ControlAddr && a.engine.GetUsername() == p.Username && a.connectToken == p.OldToken {
					a.connectToken = token
				}
				status.SetText("Recovered credential verified and privately saved. Reconnect using the bookmark.")
			})
		}()
	}
	recovered := widget.NewButton("Verify operator-recovered credential", verifyRecovered)
	body := container.NewVBox(widget.NewLabel("Saved pending attempt"), selected, widget.NewLabel("Server"), address, widget.NewLabel("Exact username"), username,
		widget.NewLabel("Current or operator-recovered personal credential"), old, consent, rotate, recovered,
		widget.NewLabel("A fresh recovery grant replaces the saved operation, retaining its candidate."),
		widget.NewLabel("Existing account ID (recovery)"), account, widget.NewLabel("Temporary recovery grant"), grant, redeem,
		container.NewHBox(check, retry), status)
	d := dialog.NewCustom("Personal Credential", "Close", container.NewVScroll(body), a.window)
	d.Resize(fyne.NewSize(580, 600))
	d.Show()
}

// Dedicated credential requests must not require an extra account session. Keep
// their single-owner reader, but explicitly release this client's slot first.
func (a *App) confirmCredentialDisconnect(retry func()) bool {
	if a.connectPending {
		dialog.ShowInformation("Connection pending", "Wait for the current connection attempt before managing credentials.", a.window)
		return true
	}
	if a.engine.GetState() == client.StateDisconnected {
		return false
	}
	attempt := a.connectAttempt
	dialog.ShowConfirm("Disconnect for credential management", "Disconnect this client before opening a dedicated credential connection? This frees its account session slot. Reconnect manually afterwards, even if the operation fails.", func(ok bool) {
		if !ok || attempt != a.connectAttempt || a.credentialBusy {
			return
		}
		a.credentialBusy = true
		done := a.engine.DisconnectAsync()
		go func() {
			if done != nil {
				<-done
			}
			fyne.Do(func() {
				a.credentialBusy = false
				if attempt == a.connectAttempt {
					retry()
				}
			})
		}()
	}, a.window)
	return true
}

func (a *App) confirmPendingCredentialTrust(err error, p client.PendingPersonalCredential, status *widget.Label) bool {
	var screenTLS *client.ScreenTLSError
	var changed *client.ServerIdentityChangedError
	if errors.As(err, &screenTLS) || !errors.As(err, &changed) || changed.Addr != p.ControlAddr || changed.Expected != p.ServerIdentity {
		return false
	}
	attempt := a.connectAttempt
	message := fmt.Sprintf("WARNING: server identity changed. Verify the new fingerprint through a trusted channel before confirming. No credential will be sent automatically. The saved candidate and account remain bound.\n\nServer: %s\nAccount: #%d %s\nPrevious: %s\nNew: %s", p.ControlAddr, p.AccountID, p.Username, p.ServerIdentity, changed.Received)
	dialog.NewCustomConfirm("Pending credential server trust changed", "Re-trust pending server", "Cancel", widget.NewLabel(message), func(ok bool) {
		if !ok || attempt != a.connectAttempt || a.credentialBusy {
			return
		}
		if err := a.bookmarks.RetrustPendingPersonalCredential(p, changed.Received); err != nil {
			_ = a.bookmarks.ReloadPersonalCredentialState()
			status.SetText("Private trust update uncertain; candidate retained. Reload and check again.")
			return
		}
		status.SetText("Confirmed trust saved with the retained candidate. Check again or explicitly retry the saved operation.")
	}, a.window).Show()
	return true
}

func (a *App) showPersonalTokenAdminDialog() {
	if a.engine.GetRole() != "admin" {
		dialog.ShowError(fmt.Errorf("administrator required"), a.window)
		return
	}
	addr, username, token, pin := a.connectServer, a.engine.GetUsername(), a.connectToken, a.bookmarks.PinForAddr(a.connectServer)
	target := widget.NewEntry()
	target.SetPlaceHolder("Existing account ID")
	status := widget.NewLabel("Inspect the target before each explicit action. Only the owner can revoke another administrator; owner recovery is local.")
	status.Wrapping = fyne.TextWrapWord
	var inspected *pb.PersonalTokenStatusResponse
	var inspectedID int64
	closed := false
	var revoke, issue, revokeGrant *widget.Button
	disableActions := func() { revoke.Disable(); issue.Disable(); revokeGrant.Disable() }
	var exchange func(*pb.ControlMessage, func(*pb.ControlMessage, error))
	exchange = func(request *pb.ControlMessage, done func(*pb.ControlMessage, error)) {
		if a.credentialBusy {
			return
		}
		if a.confirmCredentialDisconnect(func() { exchange(request, done) }) {
			return
		}
		a.credentialBusy = true
		disableActions()
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			c, _, err := client.OpenPersonalTokenControl(ctx, addr, pin, username, token)
			var response *pb.ControlMessage
			if err == nil {
				response, err = c.PersonalTokenExchange(ctx, request)
				_ = c.Close()
			}
			fyne.Do(func() { a.credentialBusy = false; done(response, err) })
		}()
	}
	mutate := func(kind string) {
		if inspected == nil || a.credentialBusy {
			return
		}
		id, err := strconv.ParseInt(strings.TrimSpace(target.Text), 10, 64)
		if err != nil || id != inspectedID {
			status.SetText("Inspect the selected target first.")
			return
		}
		snapshot := *inspected
		dialog.ShowConfirm("Change personal credential", fmt.Sprintf("%s for #%d %s (%s, scope #%d)? All target sessions retire for revocation/recovery issuance.", kind, id, snapshot.Username, snapshot.Role, snapshot.ChannelScope), func(ok bool) {
			if !ok || closed || a.credentialBusy {
				return
			}
			var request *pb.ControlMessage
			switch kind {
			case "Revoke credential":
				request = &pb.ControlMessage{RevokePersonalTokenReq: &pb.RevokePersonalTokenRequest{TargetID: id, Version: snapshot.Version}}
			case "Issue recovery grant":
				request = &pb.ControlMessage{IssuePersonalRecoveryReq: &pb.IssuePersonalRecoveryRequest{TargetID: id, Version: snapshot.Version}}
			default:
				request = &pb.ControlMessage{RevokePersonalRecoveryReq: &pb.RevokePersonalRecoveryRequest{TargetID: id, Version: snapshot.Version}}
			}
			inspected = nil
			exchange(request, func(response *pb.ControlMessage, err error) {
				if err != nil {
					status.SetText("Outcome not confirmed. Inspect before a new explicit action; no automatic retry.")
					return
				}
				if response.IssuePersonalRecoveryResp != nil {
					r := response.IssuePersonalRecoveryResp
					// Completion belongs to the action, not a canceled media generation or
					// closed dialog. Keep temporary grant handoff available until dismissed.
					secret := widget.NewPasswordEntry()
					secret.SetText(r.Grant)
					secret.Disable()
					copyGrant := widget.NewButton("Copy temporary grant", func() { a.fyneApp.Clipboard().SetContent(r.Grant) })
					dialog.NewCustom("Recovery grant issued", "Close", container.NewVBox(widget.NewLabel(fmt.Sprintf("Account #%d; expires %s. Hand this temporary grant over privately.", r.TargetID, time.Unix(r.ExpiresAt, 0).Format(time.RFC3339))), secret, copyGrant), a.window).Show()
				}
				status.SetText("Completed. Inspect again before another action.")
			})
		}, a.window)
	}
	revoke = widget.NewButton("Revoke credential", func() { mutate("Revoke credential") })
	issue = widget.NewButton("Issue recovery grant", func() { mutate("Issue recovery grant") })
	revokeGrant = widget.NewButton("Revoke recovery grant", func() { mutate("Revoke recovery grant") })
	inspect := widget.NewButton("Inspect target", func() {
		id, err := strconv.ParseInt(strings.TrimSpace(target.Text), 10, 64)
		if err != nil || id <= 0 {
			status.SetText("A positive account ID is required.")
			return
		}
		inspected = nil
		exchange(&pb.ControlMessage{PersonalTokenStatusReq: &pb.PersonalTokenStatusRequest{TargetID: id}}, func(response *pb.ControlMessage, err error) {
			if closed {
				return
			}
			if err != nil {
				status.SetText(err.Error())
				return
			}
			inspected, inspectedID = response.PersonalTokenStatusResp, id
			status.SetText(fmt.Sprintf("#%d %s - %s - scope #%d - version %d", inspected.TargetID, inspected.Username, inspected.Role, inspected.ChannelScope, inspected.Version))
			revoke.Enable()
			if inspected.Role != "admin" {
				issue.Enable()
				revokeGrant.Enable()
			}
		})
	})
	target.OnChanged = func(string) { inspected = nil; disableActions() }
	disableActions()
	d := dialog.NewCustom("Manage Personal Credentials", "Close", container.NewVBox(target, inspect, status, revoke, issue, revokeGrant), a.window)
	d.SetOnClosed(func() { closed = true })
	d.Resize(fyne.NewSize(580, 450))
	d.Show()
}
