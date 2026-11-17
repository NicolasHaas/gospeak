package ui

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"
	"github.com/NicolasHaas/gospeak/pkg/client"
)

// Exercise the handler used by Connect, with actual screen TLS failures even
// when the advertised screen address is identical to the control address.
func TestServerTrustRouting(t *testing.T) {
	for _, kind := range []string{"untrusted", "changed"} {
		for _, plane := range []string{"screen", "control"} {
			t.Run(plane+"/"+kind, func(t *testing.T) {
				isolateAudioSettingsConfig(t)
				f := test.NewApp()
				defer f.Quit()
				server := httptest.NewTLSServer(nil)
				defer server.Close()
				addr := server.Listener.Addr().String()
				pin := ""
				if kind == "changed" {
					pin = "old-pin"
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				_, err := client.NewScreenClientContext(ctx, addr, 1, "fixture-token", pin)
				if plane == "control" {
					_, err = client.NewControlClientContext(ctx, addr, pin)
				}
				if err == nil {
					t.Fatal("TLS unexpectedly succeeded")
				}
				var untrusted *client.UntrustedServerError
				var changed *client.ServerIdentityChangedError
				var received string
				if kind == "untrusted" {
					if !errors.As(err, &untrusted) {
						t.Fatalf("missing untrusted error: %v", err)
					}
					received = untrusted.Fingerprint
				} else {
					if !errors.As(err, &changed) {
						t.Fatalf("missing changed error: %v", err)
					}
					received = changed.Received
				}
				err = fmt.Errorf("engine connect: %w", err)
				store := client.NewBookmarkStore()
				store.TrustServer(addr, "original-pin")
				if err := store.Save(); err != nil {
					t.Fatal(err)
				}
				root, _ := os.UserConfigDir()
				path := filepath.Join(root, "gospeak", "servers.yaml")
				before, errRead := os.ReadFile(path)
				if errRead != nil {
					t.Fatal(errRead)
				}
				a := &App{fyneApp: f, window: f.NewWindow("trust"), bookmarks: store}
				defer a.window.Close()
				retries := 0
				handled := a.handleServerTrustError(err, addr, func(pin string) {
					retries++
					if pin != received {
						t.Errorf("retry pin = %q", pin)
					}
				})
				if plane == "screen" {
					if handled || len(a.window.Canvas().Overlays().List()) != 0 {
						t.Error("screen TLS failure offered control trust")
					}
					if store.PinForAddr(addr) != "original-pin" || retries != 0 {
						t.Error("screen failure mutated trust or retried")
					}
					after, e := os.ReadFile(path)
					if e != nil || string(after) != string(before) {
						t.Error("screen failure saved control trust")
					}
					return
				}
				if !handled {
					t.Fatal("control failure did not offer trust")
				}
				button := trustConfirmButton(a.window)
				if button == nil {
					t.Fatal("missing trust confirmation")
				}
				test.Tap(button)
				reloaded := client.NewBookmarkStore()
				if err := reloaded.Load(); err != nil {
					t.Fatal(err)
				}
				if retries != 1 || reloaded.PinForAddr(addr) != received {
					t.Fatal("control acceptance did not save and retry")
				}
			})
		}
	}
}

func TestControlTrustSaveFailureDoesNotRetry(t *testing.T) {
	isolateAudioSettingsConfig(t)
	f := test.NewApp()
	defer f.Quit()
	root, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "gospeak", "servers.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	const original = "bookmarks: ["
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	store := client.NewBookmarkStore()
	if store.Load() == nil {
		t.Fatal("malformed bookmarks loaded")
	}
	a := &App{fyneApp: f, window: f.NewWindow("trust"), bookmarks: store}
	defer a.window.Close()
	retries := 0
	a.handleServerTrustError(&client.UntrustedServerError{Addr: "fixture.test:9600", Fingerprint: "fixture-pin"}, "fixture.test:9600", func(string) { retries++ })
	button := trustConfirmButton(a.window)
	if button == nil {
		t.Fatal("missing confirmation")
	}
	test.Tap(button)
	if retries != 0 {
		t.Fatal("retried after failed save")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != original {
		t.Fatal("failed save overwrote unreadable bookmarks")
	}
}

// Keep the tested decision on the actual Connect error route, not a test-only mirror.
func TestConnectDialogUsesTrustRouting(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "app.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "showConnectDialog" {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if selector.Sel.Name == "confirmServerTrust" {
				t.Error("Connect bypasses plane-aware routing")
			}
			if selector.Sel.Name == "handleServerTrustError" {
				calls++
			}
			return true
		})
	}
	if calls != 1 {
		t.Fatalf("Connect trust routing calls = %d, want 1", calls)
	}
}

func trustConfirmButton(window fyne.Window) *widget.Button {
	var found *widget.Button
	var visit func(fyne.CanvasObject)
	visit = func(o fyne.CanvasObject) {
		switch v := o.(type) {
		case *widget.Button:
			if v.Text == "Trust and Connect" || v.Text == "Re-trust and Connect" {
				found = v
			}
		case *fyne.Container:
			for _, child := range v.Objects {
				visit(child)
			}
		case fyne.Widget:
			for _, child := range test.WidgetRenderer(v).Objects() {
				visit(child)
			}
		}
	}
	for _, overlay := range window.Canvas().Overlays().List() {
		visit(overlay)
	}
	return found
}
