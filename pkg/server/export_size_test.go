package server

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

type exportSizeStore struct {
	datastore.DataStore
	text string
}

func (s *exportSizeStore) ListChannels() ([]model.Channel, error) {
	return []model.Channel{{ID: 1, Name: "channel", Description: s.text}}, nil
}
func (s *exportSizeStore) ListUsers() ([]model.User, error) {
	return []model.User{{ID: 1, Username: s.text, Role: model.RoleUser}}, nil
}

type exportSizeFactory struct {
	datastore.DataProviderFactory
	store datastore.DataStore
}

func (f *exportSizeFactory) NonTx() datastore.DataStore { return f.store }

func TestOversizeExportReturnsGenericErrorWithoutClosing(t *testing.T) {
	for _, kind := range []string{"channels", "users"} {
		t.Run(kind, func(t *testing.T) {
			srv, st, _ := newTestServer(t)
			actor := mustCreateSession(t, srv.sessions, 1, "admin", model.RoleAdmin)
			// Legacy rows can exceed current field limits. '<' also demonstrates
			// that a raw YAML byte count is not the encoded JSON frame size.
			store := &exportSizeStore{st.NonTx(), strings.Repeat("<", protocol.MaxControlMessage/6)}
			factory := &exportSizeFactory{st, store}
			var raw []byte
			var err error
			if kind == "channels" {
				raw, err = ExportChannelsYAML(factory)
			} else {
				raw, err = ExportUsersYAML(factory)
			}
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(&pb.ControlMessage{ExportDataResp: &pb.ExportDataResponse{Type: kind, Data: string(raw)}})
			if err != nil || len(raw) > protocol.MaxControlMessage || len(encoded) <= protocol.MaxControlMessage {
				t.Fatalf("fixture must exceed encoded limit only: raw=%d encoded=%d err=%v", len(raw), len(encoded), err)
			}
			client := newControlClient(actor.ID, &nopConn{})
			srv.handleExportData(actor.ID, &pb.ExportDataRequest{Type: kind}, factory, client)
			item := <-client.sendQueue
			if item.message.ErrorResponse == nil || item.message.ErrorResponse.Message != "export too large" {
				t.Fatal("oversize export was enqueued instead of a generic error")
			}
			if client.closed {
				t.Fatal("oversize export closed admin connection")
			}
			store.text = "small"
			srv.handleExportData(actor.ID, &pb.ExportDataRequest{Type: kind}, factory, client)
			if item := <-client.sendQueue; item.message.ExportDataResp == nil {
				t.Fatal("subsequent small export did not succeed")
			}
		})
	}
}
