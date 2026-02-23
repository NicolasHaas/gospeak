// Package ui provides the Fyne-based GUI for the GoSpeak client.
package ui

import (
	"fmt"
	"image/color"
	"log/slog"
	"net"
	"sort"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/NicolasHaas/gospeak/pkg/audio"
	"github.com/NicolasHaas/gospeak/pkg/client"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
	"github.com/NicolasHaas/gospeak/pkg/version"

	// Embedded icon
	_ "embed"
)

//go:embed gospeak-icon.png
var appIconBytes []byte

// App is the main GUI application.
type App struct {
	fyneApp fyne.App
	window  fyne.Window
	engine  *client.Engine

	// UI components
	channelList   *widget.List
	statusLabel   *widget.Label
	muteBtn       *widget.Button
	deafenBtn     *widget.Button
	connectBtn    *widget.Button
	disconnectBtn *widget.Button
	serverBtn     *widget.Button
	vuMeter       *widget.ProgressBar
	vadIndicator  *widget.Label

	// Chat UI
	chatBox    *fyne.Container
	chatScroll *container.Scroll
	chatEntry  *widget.Entry

	// State
	channels []pb.ChannelInfo

	// Bookmarks & Settings
	bookmarks     *client.BookmarkStore
	settings      *client.Settings
	hotkeys       *client.GlobalHotkeys
	serverSaved   bool   // whether the current connection is saved
	connectToken  string // token used (or auto-generated) for the current connection
	connectServer string // control address of current connection
	connectVoice  string // voice address of current connection
}

// NewApp creates a new GoSpeak GUI application.
func NewApp() *App {
	// Start PortAudio init in background immediately so it's ready by the time the user connects
	audio.PreInitAudio()

	a := &App{
		fyneApp:   app.NewWithID("io.gospeak.client"),
		engine:    client.NewEngine(),
		bookmarks: client.NewBookmarkStore(),
		settings:  client.LoadSettings(),
		hotkeys:   client.NewGlobalHotkeys(),
	}
	a.bookmarks.Load() //nolint:errcheck,gosec // best-effort load
	a.engine.SetVADThreshold(a.settings.VADThreshold)
	a.window = a.fyneApp.NewWindow("GoSpeak")
	a.window.Resize(fyne.NewSize(800, 600))
	a.window.SetMaster()
	if len(appIconBytes) > 0 {
		iconRes := fyne.NewStaticResource("gospeak-icon.png", appIconBytes)
		a.window.SetIcon(iconRes)
		a.fyneApp.SetIcon(iconRes)
	}
	return a
}

// Run starts the GUI application (blocks).
func (a *App) Run() {
	a.buildUI()
	a.bindEvents()
	a.startGlobalHotkeys()
	a.window.SetCloseIntercept(func() {
		a.hotkeys.Stop()
		a.fyneApp.Quit()
	})
	a.window.ShowAndRun()
}

func (a *App) buildUI() {
	// --- Toolbar ---
	a.connectBtn = widget.NewButtonWithIcon("Connect", theme.LoginIcon(), a.showConnectDialog)
	a.disconnectBtn = widget.NewButtonWithIcon("Disconnect", theme.LogoutIcon(), func() {
		a.engine.Disconnect()
	})
	a.disconnectBtn.Disable()

	// Fixed-width mute/deafen buttons to prevent layout shift on toggle.
	a.muteBtn = widget.NewButtonWithIcon("  Mute  ", theme.VolumeMuteIcon(), func() {
		muted := !a.engine.IsMuted()
		a.engine.SetMuted(muted)
		a.updateMuteButtons()
	})
	a.muteBtn.Disable()

	a.deafenBtn = widget.NewButtonWithIcon(" Deafen ", theme.VolumeDownIcon(), func() {
		deafened := !a.engine.IsDeafened()
		a.engine.SetDeafened(deafened)
		a.updateMuteButtons()
	})
	a.deafenBtn.Disable()

	muteFixed := container.New(layout.NewGridWrapLayout(fyne.NewSize(110, 36)), a.muteBtn)
	deafenFixed := container.New(layout.NewGridWrapLayout(fyne.NewSize(110, 36)), a.deafenBtn)

	settingsBtn := widget.NewButtonWithIcon("", theme.SettingsIcon(), a.showSettingsDialog)
	a.serverBtn = widget.NewButton("Server Settings", a.showServerSettings)
	a.serverBtn.Hide()
	helpBtn := widget.NewButtonWithIcon("", theme.InfoIcon(), a.showHelpDialog)

	toolbar := container.NewHBox(
		a.connectBtn,
		a.disconnectBtn,
		layout.NewSpacer(),
		a.serverBtn,
		settingsBtn,
		helpBtn,
		muteFixed,
		deafenFixed,
	)

	// --- VU Meter and VAD ---
	a.vuMeter = widget.NewProgressBar()
	a.vuMeter.Min = 0
	a.vuMeter.Max = 1
	a.vadIndicator = widget.NewLabel("VAD: --")
	a.vadIndicator.TextStyle = fyne.TextStyle{Bold: true}

	audioBar := container.NewBorder(nil, nil, a.vadIndicator, nil, a.vuMeter)

	// --- Channel/User List (Sidebar) ---
	a.channelList = widget.NewList(
		func() int { return a.channelListLen() },
		func() fyne.CanvasObject {
			indent := canvas.NewRectangle(color.Transparent)
			indent.SetMinSize(fyne.NewSize(0, 0))
			icon := widget.NewIcon(theme.FolderIcon())
			label := widget.NewLabel("Channel / User placeholder")
			gearBtn := widget.NewButtonWithIcon("", theme.SettingsIcon(), nil)
			gearBtn.Importance = widget.LowImportance
			gearBtn.Hide()
			return container.NewHBox(indent, icon, label, layout.NewSpacer(), gearBtn)
		},
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			a.updateChannelListItem(id, obj)
		},
	)
	a.channelList.OnSelected = func(id widget.ListItemID) {
		a.onChannelListSelect(id)
	}

	sidebar := container.NewBorder(
		widget.NewLabelWithStyle("Channels", fyne.TextAlignCenter, fyne.TextStyle{Bold: true}),
		nil, nil, nil,
		a.channelList,
	)

	// --- Status ---
	a.statusLabel = widget.NewLabel("Disconnected")
	a.statusLabel.TextStyle = fyne.TextStyle{Italic: true}

	versionLabel := widget.NewLabel(version.String())
	versionLabel.TextStyle = fyne.TextStyle{Italic: true}
	versionLabel.Importance = widget.LowImportance

	// --- Chat panel (right side) ---
	a.chatBox = container.NewVBox()
	a.chatScroll = container.NewVScroll(a.chatBox)

	a.chatEntry = widget.NewEntry()
	a.chatEntry.SetPlaceHolder("Type a message... (Enter to send)")
	a.chatEntry.Disable()
	a.chatEntry.OnSubmitted = func(text string) {
		text = strings.TrimSpace(text)
		if text == "" {
			return
		}
		if err := a.engine.SendChat(text); err != nil {
			slog.Debug("send chat error", "err", err)
		}
		a.chatEntry.SetText("")
	}

	chatHeader := widget.NewLabelWithStyle("Chat", fyne.TextAlignCenter, fyne.TextStyle{Bold: true})
	chatPanel := container.NewBorder(chatHeader, a.chatEntry, nil, nil, a.chatScroll)

	// --- Main layout ---
	mainArea := container.NewHSplit(sidebar, chatPanel)
	mainArea.SetOffset(0.3)

	statusBar := container.NewHBox(a.statusLabel, layout.NewSpacer(), versionLabel)

	content := container.NewBorder(
		container.NewVBox(toolbar, audioBar),
		statusBar,
		nil, nil,
		mainArea,
	)

	a.window.SetContent(content)
}

func (a *App) bindEvents() {
	a.engine.OnStateChange = func(state client.State) {
		fyne.Do(func() {
			switch state {
			case client.StateDisconnected:
				a.statusLabel.SetText("Disconnected")
				a.connectBtn.Enable()
				a.disconnectBtn.Disable()
				a.muteBtn.Disable()
				a.deafenBtn.Disable()
				a.updateMuteButtons()
				a.serverBtn.Hide()
				a.chatEntry.Disable()
				a.channels = nil
				a.channelList.Refresh()
			case client.StateConnecting:
				a.statusLabel.SetText("Connecting...")
				a.connectBtn.Disable()
			case client.StateConnected:
				a.statusLabel.SetText(fmt.Sprintf("Connected as %s (%s)", a.engine.GetUsername(), a.engine.GetRole()))
				a.connectBtn.Disable()
				a.disconnectBtn.Enable()
				a.muteBtn.Enable()
				a.deafenBtn.Enable()
				a.updateMuteButtons()
				a.chatEntry.Enable()
				role := a.engine.GetRole()
				if role == "admin" || role == "moderator" {
					a.serverBtn.Show()
				}
			}
		})
	}

	a.engine.OnChannelsUpdate = func(channels []pb.ChannelInfo) {
		fyne.Do(func() {
			a.channels = channels
			a.channelList.Refresh()
		})
	}

	a.engine.OnError = func(err error) {
		fyne.Do(func() {
			dialog.ShowError(err, a.window)
		})
	}

	a.engine.OnVoiceActivity = func(active bool) {
		fyne.Do(func() {
			if active {
				a.vadIndicator.SetText("VAD: ACTIVE")
			} else {
				a.vadIndicator.SetText("VAD: --")
			}
		})
	}

	a.engine.OnRMSLevel = func(level float64) {
		normalized := level / 5000.0
		if normalized > 1 {
			normalized = 1
		}
		fyne.Do(func() {
			a.vuMeter.SetValue(normalized)
		})
	}

	a.engine.OnDisconnect = func(reason string) {
		fyne.Do(func() {
			if reason != "user disconnected" {
				dialog.ShowInformation("Disconnected", reason, a.window)
			}
		})
	}

	a.engine.OnChatMessage = func(channelID int64, sender, text string, ts int64) {
		fyne.Do(func() {
			t := time.Unix(ts, 0)
			lbl := widget.NewLabel(fmt.Sprintf("[%s] %s: %s", t.Format("15:04"), sender, text))
			lbl.Wrapping = fyne.TextWrapWord
			a.chatBox.Add(lbl)
			// Keep at most 500 messages
			if len(a.chatBox.Objects) > 500 {
				a.chatBox.Objects = a.chatBox.Objects[len(a.chatBox.Objects)-500:]
				a.chatBox.Refresh()
			}
			a.chatScroll.ScrollToBottom()
		})
	}

	a.engine.OnTokenCreated = func(token string) {
		fyne.Do(func() {
			entry := widget.NewEntry()
			entry.SetText(token)
			entry.Disable()
			d := dialog.NewCustom("Token Created", "Close", container.NewVBox(
				widget.NewLabel("Share this token with users to let them join:"),
				entry,
			), a.window)
			d.Resize(fyne.NewSize(450, 150))
			d.Show()
		})
	}

	a.engine.OnRoleChanged = func(success bool, message string) {
		fyne.Do(func() {
			if success {
				dialog.ShowInformation("Role Updated", message, a.window)
				// Update server button visibility based on new role
				role := a.engine.GetRole()
				if role == "admin" || role == "moderator" {
					a.serverBtn.Show()
				} else {
					a.serverBtn.Hide()
				}
				// Warn if promoted and server not saved
				if !a.serverSaved && (role == "admin" || role == "moderator") {
					dialog.ShowInformation("Warning",
						"You have been promoted but this server is not saved.\n"+
							"Your token is needed to keep your role on reconnect.\n"+
							"Use Connect → Save Server to bookmark this server.",
						a.window)
				}
			} else {
				dialog.ShowError(fmt.Errorf("%s", message), a.window)
			}
		})
	}

	a.engine.OnAutoToken = func(token string) {
		a.connectToken = token
		fyne.Do(func() {
			if a.serverSaved {
				a.saveCurrentBookmark(a.engine.GetUsername())
			}

			message := "The server generated a personal token for you.\n"
			if a.serverSaved {
				message += "This server is saved, so the token is stored in your bookmark.\n"
			} else {
				message += "Save this token or enable 'Save Server' to keep it.\n"
			}
			message += "Without it, you will lose your identity on reconnect."

			entry := widget.NewEntry()
			entry.SetText(token)
			entry.Disable()
			copyBtn := widget.NewButtonWithIcon("", theme.ContentCopyIcon(), func() {
				a.window.Clipboard().SetContent(token)
			})
			copyBtn.Importance = widget.LowImportance

			content := container.NewVBox(
				widget.NewLabel(message),
				container.NewBorder(nil, nil, nil, copyBtn, entry),
			)
			d := dialog.NewCustom("Personal Token", "Close", content, a.window)
			d.Resize(fyne.NewSize(520, 180))
			d.Show()
		})
	}

	a.engine.OnExportData = func(dataType, data string) {
		fyne.Do(func() {
			a.showExportResult(dataType, data)
		})
	}

	a.engine.OnImportResult = func(success bool, message string) {
		fyne.Do(func() {
			if success {
				dialog.ShowInformation("Import", message, a.window)
			} else {
				dialog.ShowError(fmt.Errorf("%s", message), a.window)
			}
		})
	}
}

func (a *App) startGlobalHotkeys() {
	a.hotkeys.SetKeys(a.settings.MuteKey, a.settings.DeafenKey)
	a.hotkeys.OnMuteToggle = func() {
		if a.engine.GetState() == client.StateConnected {
			muted := !a.engine.IsMuted()
			a.engine.SetMuted(muted)
			fyne.Do(func() { a.updateMuteButtons() })
		}
	}
	a.hotkeys.OnDeafenToggle = func() {
		if a.engine.GetState() == client.StateConnected {
			deafened := !a.engine.IsDeafened()
			a.engine.SetDeafened(deafened)
			fyne.Do(func() { a.updateMuteButtons() })
		}
	}
	a.hotkeys.Start()
}

const (
	defaultServerHost  = "gospeak.haas-nicolas.ch"
	defaultControlPort = "9600"
	defaultVoicePort   = "9601"
)

func normalizeAddr(input, defaultPort string) (string, string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", "", fmt.Errorf("address is required")
	}

	host, port, err := net.SplitHostPort(input)
	if err == nil {
		return net.JoinHostPort(host, port), host, nil
	}

	if strings.Count(input, ":") > 1 {
		trimmed := strings.Trim(input, "[]")
		return net.JoinHostPort(trimmed, defaultPort), trimmed, nil
	}

	return net.JoinHostPort(input, defaultPort), input, nil
}

func displayControlInput(controlAddr string) string {
	host, port, err := net.SplitHostPort(controlAddr)
	if err != nil {
		return controlAddr
	}
	if port == defaultControlPort {
		return host
	}
	return controlAddr
}

func deriveVoiceAddr(controlAddr string) string {
	host, _, err := net.SplitHostPort(controlAddr)
	if err != nil || host == "" {
		return controlAddr
	}
	return net.JoinHostPort(host, defaultVoicePort)
}

func (a *App) showConnectDialog() {
	serverEntry := widget.NewEntry()
	serverEntry.SetPlaceHolder(defaultServerHost)
	serverEntry.SetText(defaultServerHost)

	voiceEntry := widget.NewEntry()
	voiceEntry.SetPlaceHolder(fmt.Sprintf("%s:%s", defaultServerHost, defaultVoicePort))

	usernameEntry := widget.NewEntry()
	usernameEntry.SetPlaceHolder("Your display name")

	tokenEntry := widget.NewPasswordEntry()
	tokenEntry.SetPlaceHolder("Invite token (optional for open servers)")
	pasteTokenBtn := widget.NewButton("Paste", func() {
		content := a.window.Clipboard().Content()
		if content != "" {
			tokenEntry.SetText(content)
		}
	})
	pasteTokenBtn.Importance = widget.LowImportance
	pasteTokenBtn.Hide()

	updatePasteVisibility := func() {
		if strings.TrimSpace(tokenEntry.Text) == "" {
			pasteTokenBtn.Show()
		} else {
			pasteTokenBtn.Hide()
		}
	}

	tokenEntry.OnChanged = func(_ string) {
		updatePasteVisibility()
	}
	updatePasteVisibility()

	saveCheck := widget.NewCheck("Save server (bookmark)", nil)

	advancedContainer := container.NewVBox(
		widget.NewLabel("Voice (optional)"),
		voiceEntry,
	)
	advancedAccordion := widget.NewAccordion(widget.NewAccordionItem("Advanced", advancedContainer))

	sortedBookmarks := make([]client.Bookmark, len(a.bookmarks.Bookmarks))
	copy(sortedBookmarks, a.bookmarks.Bookmarks)
	sort.Slice(sortedBookmarks, func(i, j int) bool {
		if sortedBookmarks[i].LastUsed == sortedBookmarks[j].LastUsed {
			return sortedBookmarks[i].Name < sortedBookmarks[j].Name
		}
		return sortedBookmarks[i].LastUsed > sortedBookmarks[j].LastUsed
	})

	labelToBookmark := make(map[string]client.Bookmark, len(sortedBookmarks))
	savedNames := make([]string, 0, len(sortedBookmarks)+1)
	savedNames = append(savedNames, "(New Server)")
	for _, b := range sortedBookmarks {
		label := fmt.Sprintf("%s (%s)", b.Name, displayControlInput(b.ControlAddr))
		savedNames = append(savedNames, label)
		labelToBookmark[label] = b
	}

	var selectedBookmark *client.Bookmark
	resetToNew := func() {
		selectedBookmark = nil
		serverEntry.SetText(defaultServerHost)
		voiceEntry.SetText("")
		usernameEntry.SetText("")
		tokenEntry.SetText("")
		saveCheck.SetChecked(false)
		advancedAccordion.Close(0)
	}
	applyBookmark := func(b client.Bookmark) {
		selectedBookmark = &b
		serverEntry.SetText(displayControlInput(b.ControlAddr))
		usernameEntry.SetText(b.Username)
		tokenEntry.SetText(b.Token)
		saveCheck.SetChecked(true)

		derivedVoice := deriveVoiceAddr(b.ControlAddr)
		if b.VoiceAddr != "" && b.VoiceAddr != derivedVoice {
			voiceEntry.SetText(b.VoiceAddr)
			advancedAccordion.Open(0)
			return
		}
		voiceEntry.SetText("")
		advancedAccordion.Close(0)
	}

	savedSelect := widget.NewSelect(savedNames, func(selected string) {
		if selected == "(New Server)" {
			resetToNew()
			return
		}
		if b, ok := labelToBookmark[selected]; ok {
			applyBookmark(b)
		}
	})

	if len(sortedBookmarks) > 0 {
		savedSelect.SetSelected(savedNames[1])
	} else {
		savedSelect.SetSelected("(New Server)")
	}

	tokenRow := container.NewBorder(nil, nil, nil, pasteTokenBtn, tokenEntry)

	content := container.NewVBox(
		widget.NewLabel("Saved"),
		savedSelect,
		widget.NewSeparator(),
		widget.NewLabel("Server"),
		serverEntry,
		advancedAccordion,
		widget.NewSeparator(),
		widget.NewLabel("Username"),
		usernameEntry,
		widget.NewLabel("Token"),
		tokenRow,
		saveCheck,
	)

	d := dialog.NewCustomConfirm(
		"Connect to Server",
		"Connect",
		"Cancel",
		content,
		func(ok bool) {
			if !ok {
				return
			}
			controlAddr, controlHost, err := normalizeAddr(serverEntry.Text, defaultControlPort)
			if err != nil {
				dialog.ShowError(err, a.window)
				return
			}
			username := strings.TrimSpace(usernameEntry.Text)
			if controlHost == "" || username == "" {
				dialog.ShowError(fmt.Errorf("server and username are required"), a.window)
				return
			}

			voiceAddr := ""
			if strings.TrimSpace(voiceEntry.Text) != "" {
				voiceAddr, _, err = normalizeAddr(voiceEntry.Text, defaultVoicePort)
				if err != nil {
					dialog.ShowError(err, a.window)
					return
				}
			} else {
				voiceAddr = net.JoinHostPort(controlHost, defaultVoicePort)
			}

			token := tokenEntry.Text
			a.serverSaved = saveCheck.Checked
			a.connectToken = token
			a.connectServer = controlAddr
			a.connectVoice = voiceAddr

			go func() {
				if err := a.engine.Connect(controlAddr, voiceAddr, token, username); err != nil {
					slog.Error("connect failed", "err", err)
					fyne.Do(func() {
						dialog.ShowError(fmt.Errorf("connection failed: %v", err), a.window)
					})
					return
				}
				if saveCheck.Checked {
					a.saveCurrentBookmark(username)
					return
				}
				if selectedBookmark != nil {
					if a.bookmarks.Touch(selectedBookmark.ControlAddr, selectedBookmark.Username, time.Now().Unix()) {
						if err := a.bookmarks.Save(); err != nil {
							slog.Error("failed to save bookmark", "err", err)
						}
					}
				}
			}()
		},
		a.window,
	)
	d.Resize(fyne.NewSize(420, 420))
	d.Show()
}

// ----- Admin / Settings dialogs -----

func (a *App) showServerSettings() {
	role := a.engine.GetRole()
	var sections []fyne.CanvasObject

	// --- Create Invite Token (admin/mod) ---
	if role == "admin" || role == "moderator" {
		roleSelect := widget.NewSelect([]string{"user", "moderator", "admin"}, nil)
		roleSelect.SetSelected("user")
		maxUsesEntry := widget.NewEntry()
		maxUsesEntry.SetText("10")
		expiresEntry := widget.NewEntry()
		expiresEntry.SetText("86400")

		createTokenBtn := widget.NewButton("Create Token", func() {
			var maxUses int
			_, _ = fmt.Sscanf(maxUsesEntry.Text, "%d", &maxUses)
			var expires int64
			_, _ = fmt.Sscanf(expiresEntry.Text, "%d", &expires)
			if err := a.engine.CreateToken(roleSelect.Selected, maxUses, expires); err != nil {
				dialog.ShowError(err, a.window)
			}
		})

		sections = append(sections,
			widget.NewLabelWithStyle("Create Invite Token", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			container.NewHBox(widget.NewLabel("Role:"), roleSelect),
			container.NewHBox(widget.NewLabel("Max Uses:"), maxUsesEntry),
			container.NewHBox(widget.NewLabel("Expires (sec):"), expiresEntry),
			createTokenBtn,
			widget.NewSeparator(),
		)
	}

	// --- Create Channel (admin/mod) ---
	if role == "admin" || role == "moderator" {
		chNameEntry := widget.NewEntry()
		chNameEntry.SetPlaceHolder("Channel name")
		chDescEntry := widget.NewEntry()
		chDescEntry.SetPlaceHolder("Description (optional)")
		chMaxEntry := widget.NewEntry()
		chMaxEntry.SetText("0")
		chAllowSub := widget.NewCheck("Allow sub-channels", nil)

		createChanBtn := widget.NewButton("Create Channel", func() {
			name := strings.TrimSpace(chNameEntry.Text)
			if name == "" {
				return
			}
			var maxUsers int
			_, _ = fmt.Sscanf(chMaxEntry.Text, "%d", &maxUsers)
			if err := a.engine.CreateChannelAdvanced(name, chDescEntry.Text, maxUsers, 0, false, chAllowSub.Checked); err != nil {
				dialog.ShowError(err, a.window)
			}
		})

		sections = append(sections,
			widget.NewLabelWithStyle("Create Channel", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			chNameEntry,
			chDescEntry,
			container.NewHBox(widget.NewLabel("Max Users (0=unlimited):"), chMaxEntry),
			chAllowSub,
			createChanBtn,
			widget.NewSeparator(),
		)
	}

	// --- Export / Import (admin) ---
	if role == "admin" {
		exportChBtn := widget.NewButton("Export Channels (YAML)", func() {
			if err := a.engine.ExportData("channels"); err != nil {
				dialog.ShowError(err, a.window)
			}
		})
		exportUsersBtn := widget.NewButton("Export Users (YAML)", func() {
			if err := a.engine.ExportData("users"); err != nil {
				dialog.ShowError(err, a.window)
			}
		})
		importBtn := widget.NewButton("Import Channels (YAML)", func() {
			a.showImportDialog()
		})

		sections = append(sections,
			widget.NewLabelWithStyle("Export / Import", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			exportChBtn,
			exportUsersBtn,
			importBtn,
		)
	}

	if len(sections) == 0 {
		dialog.ShowInformation("Server Settings", "No server actions available for your role.", a.window)
		return
	}

	scroll := container.NewVScroll(container.NewVBox(sections...))
	scroll.SetMinSize(fyne.NewSize(380, 400))

	d := dialog.NewCustom("Server Settings", "Close", scroll, a.window)
	d.Resize(fyne.NewSize(420, 480))
	d.Show()
}

func (a *App) showImportDialog() {
	yamlEntry := widget.NewMultiLineEntry()
	yamlEntry.SetPlaceHolder("Paste YAML here...\nExample:\nchannels:\n  - name: Gaming\n    allow_sub_channels: true\n  - name: Music")
	yamlEntry.SetMinRowsVisible(12)

	d := dialog.NewForm("Import Channels", "Import", "Cancel",
		[]*widget.FormItem{
			widget.NewFormItem("YAML", yamlEntry),
		},
		func(ok bool) {
			if !ok {
				return
			}
			data := strings.TrimSpace(yamlEntry.Text)
			if data == "" {
				return
			}
			if err := a.engine.ImportChannels(data); err != nil {
				dialog.ShowError(err, a.window)
			}
		}, a.window)
	d.Resize(fyne.NewSize(500, 400))
	d.Show()
}

func (a *App) showExportResult(dataType, data string) {
	entry := widget.NewMultiLineEntry()
	entry.SetText(data)
	entry.SetMinRowsVisible(15)

	title := "Export: " + dataType
	d := dialog.NewCustom(title, "Close", container.NewVBox(
		widget.NewLabel("Copy the YAML below:"),
		entry,
	), a.window)
	d.Resize(fyne.NewSize(500, 450))
	d.Show()
}

func (a *App) showSettingsDialog() {
	// Audio input devices
	inputDevices, _ := audio.ListInputDevices()
	inputNames := make([]string, 0, len(inputDevices)+1)
	inputNames = append(inputNames, "(Default)")
	for _, d := range inputDevices {
		inputNames = append(inputNames, d.Name)
	}
	inputSelect := widget.NewSelect(inputNames, nil)
	if a.settings.AudioInput != "" {
		inputSelect.SetSelected(a.settings.AudioInput)
	} else {
		inputSelect.SetSelected("(Default)")
	}

	// Audio output devices
	outputDevices, _ := audio.ListOutputDevices()
	outputNames := make([]string, 0, len(outputDevices)+1)
	outputNames = append(outputNames, "(Default)")
	for _, d := range outputDevices {
		outputNames = append(outputNames, d.Name)
	}
	outputSelect := widget.NewSelect(outputNames, nil)
	if a.settings.AudioOutput != "" {
		outputSelect.SetSelected(a.settings.AudioOutput)
	} else {
		outputSelect.SetSelected("(Default)")
	}

	// VAD threshold slider
	vadSlider := widget.NewSlider(50, 3000)
	vadSlider.Value = a.settings.VADThreshold
	vadSlider.Step = 25
	vadLabel := widget.NewLabel(fmt.Sprintf("VAD Threshold: %.0f", a.settings.VADThreshold))
	vadSlider.OnChanged = func(v float64) {
		vadLabel.SetText(fmt.Sprintf("VAD Threshold: %.0f", v))
	}

	// Hotkey configuration
	keyOptions := []string{"F1", "F2", "F3", "F4", "F5", "F6", "F7", "F8", "F9", "F10", "F11", "F12"}
	muteKeySelect := widget.NewSelect(keyOptions, nil)
	muteKeySelect.SetSelected(a.settings.MuteKey)
	deafenKeySelect := widget.NewSelect(keyOptions, nil)
	deafenKeySelect.SetSelected(a.settings.DeafenKey)

	content := container.NewVBox(
		widget.NewLabelWithStyle("Audio", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewSeparator(),
		widget.NewLabel("Input Device:"),
		inputSelect,
		widget.NewLabel("Output Device:"),
		outputSelect,
		widget.NewSeparator(),
		vadLabel,
		vadSlider,
		widget.NewLabel("Lower = more sensitive, Higher = less sensitive"),
		widget.NewSeparator(),
		widget.NewLabelWithStyle("Hotkeys (global, works in background)", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewSeparator(),
		container.NewHBox(widget.NewLabel("Mute:"), muteKeySelect),
		container.NewHBox(widget.NewLabel("Deafen:"), deafenKeySelect),
	)

	d := dialog.NewCustomConfirm("Settings", "Apply", "Cancel", content,
		func(ok bool) {
			if !ok {
				return
			}
			a.engine.SetVADThreshold(vadSlider.Value)

			a.settings.VADThreshold = vadSlider.Value
			a.settings.MuteKey = muteKeySelect.Selected
			a.settings.DeafenKey = deafenKeySelect.Selected
			if inputSelect.Selected != "(Default)" {
				a.settings.AudioInput = inputSelect.Selected
			} else {
				a.settings.AudioInput = ""
			}
			if outputSelect.Selected != "(Default)" {
				a.settings.AudioOutput = outputSelect.Selected
			} else {
				a.settings.AudioOutput = ""
			}

			if err := a.settings.Save(); err != nil {
				slog.Error("save settings", "err", err)
			}

			// Update global hotkeys live
			a.hotkeys.SetKeys(a.settings.MuteKey, a.settings.DeafenKey)

			dialog.ShowInformation("Settings", "Settings saved. Audio device changes apply on next connection.", a.window)
		}, a.window)
	d.Resize(fyne.NewSize(450, 520))
	d.Show()
}

// ----- Channel/User list helpers -----

// flatItem represents a node in the flattened channel tree.
type flatItem struct {
	isChannel bool
	channel   pb.ChannelInfo
	user      pb.UserInfo
	channelID int64
	depth     int // indentation level (0=root, 1=sub-channel, etc.)
}

func (a *App) flattenChannels() []flatItem {
	var items []flatItem
	// Build a map of parentID -> children for tree ordering
	childMap := make(map[int64][]pb.ChannelInfo)
	for _, ch := range a.channels {
		childMap[ch.ParentID] = append(childMap[ch.ParentID], ch)
	}
	// Recursively flatten starting from root (ParentID=0)
	var flatten func(parentID int64, depth int)
	flatten = func(parentID int64, depth int) {
		for _, ch := range childMap[parentID] {
			items = append(items, flatItem{isChannel: true, channel: ch, channelID: ch.ID, depth: depth})
			for _, u := range ch.Users {
				items = append(items, flatItem{isChannel: false, user: u, channelID: ch.ID, depth: depth})
			}
			flatten(ch.ID, depth+1)
		}
	}
	flatten(0, 0)
	return items
}

func (a *App) channelListLen() int {
	return len(a.flattenChannels())
}

func (a *App) getItem(index int) flatItem {
	items := a.flattenChannels()
	if index < len(items) {
		return items[index]
	}
	return flatItem{}
}

func (a *App) updateChannelListItem(id widget.ListItemID, obj fyne.CanvasObject) {
	box := obj.(*fyne.Container)
	indent := box.Objects[0].(*canvas.Rectangle)
	icon := box.Objects[1].(*widget.Icon)
	label := box.Objects[2].(*widget.Label)
	// Objects[3] is layout spacer
	gearBtn := box.Objects[4].(*widget.Button)

	item := a.getItem(id)
	currentChannelID := a.engine.GetChannelID()
	currentUser := a.engine.GetUsername()
	indent.SetMinSize(fyne.NewSize(float32(item.depth)*20, 1))
	indent.Refresh()

	if item.isChannel {
		if item.depth > 0 {
			icon.SetResource(theme.FolderOpenIcon())
		} else {
			icon.SetResource(theme.FolderIcon())
		}
		userCount := len(item.channel.Users)
		name := item.channel.Name
		if item.channel.IsTemp {
			name = "~ " + name
		}
		if item.channelID == currentChannelID {
			name = "▶ " + name
			label.TextStyle = fyne.TextStyle{Bold: true, Italic: true}
		} else {
			label.TextStyle = fyne.TextStyle{Bold: true}
		}
		label.SetText(fmt.Sprintf("%s (%d)", name, userCount))

		// Show gear icon when user has channel actions available
		role := a.engine.GetRole()
		showGear := a.engine.GetState() == client.StateConnected &&
			(item.channel.AllowSubChannels || role == "admin" || role == "moderator")
		if showGear {
			ch := item.channel
			gearBtn.OnTapped = func() {
				a.showChannelSettingsDialog(ch)
			}
			gearBtn.Show()
		} else {
			gearBtn.Hide()
		}
	} else {
		icon.SetResource(theme.AccountIcon())
		status := ""
		if item.user.Muted {
			status += " [M]"
		}
		if item.user.Deafened {
			status += " [D]"
		}
		roleTag := ""
		switch item.user.Role {
		case "admin":
			roleTag = " *"
		case "moderator":
			roleTag = " +"
		}
		labelText := fmt.Sprintf("%s%s%s", item.user.Username, roleTag, status)
		if item.user.Username == currentUser && item.channelID == currentChannelID {
			labelText += " (you)"
			label.TextStyle = fyne.TextStyle{Italic: true}
		} else {
			label.TextStyle = fyne.TextStyle{}
		}
		label.SetText(labelText)
		gearBtn.Hide()
	}
	label.Refresh()
}

func (a *App) onChannelListSelect(id widget.ListItemID) {
	item := a.getItem(id)
	if item.isChannel {
		if a.engine.GetState() != client.StateConnected {
			return
		}
		if err := a.engine.JoinChannel(item.channelID); err != nil {
			dialog.ShowError(err, a.window)
		}
		a.chatBox.Objects = nil
		a.chatBox.Refresh()
		return
	}

	// Clicking on a user shows admin actions (for admin/mod)
	if a.engine.GetRole() == "admin" || a.engine.GetRole() == "moderator" {
		a.showUserContextMenu(item.user)
	}
}

func (a *App) showUserContextMenu(user pb.UserInfo) {
	role := a.engine.GetRole()
	var buttons []fyne.CanvasObject

	if role == "admin" {
		roleSelect := widget.NewSelect([]string{"user", "moderator", "admin"}, nil)
		roleSelect.SetSelected(user.Role)
		setRoleBtn := widget.NewButton("Set Role", func() {
			_ = a.engine.SetUserRole(user.ID, roleSelect.Selected)
		})
		buttons = append(buttons,
			widget.NewLabelWithStyle(fmt.Sprintf("User: %s (role: %s)", user.Username, user.Role), fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
			widget.NewSeparator(),
			container.NewHBox(widget.NewLabel("Change role:"), roleSelect, setRoleBtn),
		)
	}

	if role == "admin" || role == "moderator" {
		kickBtn := widget.NewButton("Kick User", func() {
			dialog.ShowConfirm("Kick User", fmt.Sprintf("Kick %s?", user.Username), func(ok bool) {
				if ok {
					_ = a.engine.KickUser(user.ID, "kicked by "+a.engine.GetUsername())
				}
			}, a.window)
		})
		buttons = append(buttons, kickBtn)
	}

	if role == "admin" {
		banBtn := widget.NewButton("Ban User (1h)", func() {
			dialog.ShowConfirm("Ban User", fmt.Sprintf("Ban %s for 1 hour?", user.Username), func(ok bool) {
				if ok {
					_ = a.engine.BanUser(user.ID, "banned by "+a.engine.GetUsername(), 3600)
				}
			}, a.window)
		})
		buttons = append(buttons, banBtn)
	}

	if len(buttons) == 0 {
		return
	}

	d := dialog.NewCustom("User Actions", "Close", container.NewVBox(buttons...), a.window)
	d.Resize(fyne.NewSize(400, 250))
	d.Show()
}

func (a *App) updateMuteButtons() {
	if a.engine.IsMuted() {
		a.muteBtn.SetText(" Unmute ")
		a.muteBtn.Importance = widget.DangerImportance
	} else {
		a.muteBtn.SetText("  Mute  ")
		a.muteBtn.Importance = widget.MediumImportance
	}
	a.muteBtn.Refresh()
	if a.engine.IsDeafened() {
		a.deafenBtn.SetText("Undeafen")
		a.deafenBtn.Importance = widget.DangerImportance
	} else {
		a.deafenBtn.SetText(" Deafen ")
		a.deafenBtn.Importance = widget.MediumImportance
	}
	a.deafenBtn.Refresh()
}

func (a *App) showChannelSettingsDialog(channel pb.ChannelInfo) {
	role := a.engine.GetRole()
	var items []fyne.CanvasObject

	items = append(items,
		widget.NewLabelWithStyle(channel.Name, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		widget.NewSeparator(),
	)

	// Join button (always shown)
	joinBtn := widget.NewButton("Join Channel", func() {
		if err := a.engine.JoinChannel(channel.ID); err != nil {
			dialog.ShowError(err, a.window)
		}
		a.chatBox.Objects = nil
		a.chatBox.Refresh()
	})
	items = append(items, joinBtn)

	// Create sub-channel (if allowed)
	if channel.AllowSubChannels {
		items = append(items, widget.NewSeparator())
		nameEntry := widget.NewEntry()
		nameEntry.SetPlaceHolder("Sub-channel name")
		createBtn := widget.NewButton("Create Sub-Channel", func() {
			name := strings.TrimSpace(nameEntry.Text)
			if name == "" {
				return
			}
			if err := a.engine.CreateSubChannel(channel.ID, name); err != nil {
				dialog.ShowError(err, a.window)
			}
		})
		items = append(items,
			widget.NewLabel("Create temporary sub-channel:"),
			nameEntry, createBtn,
		)
	}

	// Admin: delete channel (not Lobby)
	if (role == "admin" || role == "moderator") && channel.Name != "Lobby" {
		items = append(items, widget.NewSeparator())
		delBtn := widget.NewButton("Delete Channel", func() {
			dialog.ShowConfirm("Delete Channel",
				fmt.Sprintf("Delete channel %q?", channel.Name),
				func(ok bool) {
					if ok {
						if err := a.engine.DeleteChannel(channel.ID); err != nil {
							dialog.ShowError(err, a.window)
						}
					}
				}, a.window)
		})
		delBtn.Importance = widget.DangerImportance
		items = append(items, delBtn)
	}

	d := dialog.NewCustom("Channel Settings", "Close", container.NewVBox(items...), a.window)
	d.Resize(fyne.NewSize(350, 280))
	d.Show()
}

func (a *App) showHelpDialog() {
	helpText := "GoSpeak — Voice Communication\n\n" +
		"CHANNEL LIST\n" +
		"  Folder icon      — Channel with user count\n" +
		"  ~ prefix         — Temporary sub-channel\n" +
		"  Gear icon        — Click for channel settings\n\n" +
		"USER INDICATORS\n" +
		"  * Username       — Admin\n" +
		"  + Username       — Moderator\n" +
		"  [M]              — Muted\n" +
		"  [D]              — Deafened\n\n" +
		"TOOLBAR\n" +
		"  Server Settings  — Tokens, channels, import/export (admin/mod)\n" +
		"  Gear icon        — Audio & hotkey settings\n" +
		"  Info icon        — This help dialog\n\n" +
		"HOTKEYS (global, work in background)\n" +
		fmt.Sprintf("  %-16s — Toggle Mute\n", a.settings.MuteKey) +
		fmt.Sprintf("  %-16s — Toggle Deafen\n", a.settings.DeafenKey) +
		"  (Configurable in Settings)\n\n" +
		"CHAT\n" +
		"  Messages are per-channel.\n" +
		"  Click a channel to join and chat.\n" +
		"  Press Enter to send a message."

	label := widget.NewLabel(helpText)
	label.TextStyle = fyne.TextStyle{Monospace: true}
	scroll := container.NewVScroll(label)
	scroll.SetMinSize(fyne.NewSize(410, 380))

	d := dialog.NewCustom("Help", "Close", scroll, a.window)
	d.Resize(fyne.NewSize(460, 450))
	d.Show()
}

func (a *App) saveCurrentBookmark(username string) {
	name := a.connectServer
	if username != "" {
		name = username + "@" + a.connectServer
	}
	a.bookmarks.Add(client.Bookmark{
		Name:        name,
		ControlAddr: a.connectServer,
		VoiceAddr:   a.connectVoice,
		Username:    username,
		Token:       a.connectToken,
		LastUsed:    time.Now().Unix(),
	})
	if err := a.bookmarks.Save(); err != nil {
		slog.Error("failed to save bookmark", "err", err)
	}
}
