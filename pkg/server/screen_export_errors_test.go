package server

import (
	"testing"

	"github.com/NicolasHaas/gospeak/pkg/datastore"
	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

func TestExportErrorsDoNotEchoInputOrInternals(t *testing.T) {
	srv, st, _ := newTestServer(t)
	actor := mustCreateSession(t, srv.sessions, 1, "admin", model.RoleAdmin)
	conn := &bufferConn{}
	srv.handleExportData(actor.ID, &pb.ExportDataRequest{Type: "attacker\u202e-secret"}, st, conn)
	response, err := protocol.ReadControlMessage(conn)
	if err != nil || response.ErrorResponse == nil || response.ErrorResponse.Message != "unknown export type" {
		t.Fatalf("response=%#v err=%v", response, err)
	}
}

func TestExportDatastoreFailureIsGeneric(t *testing.T) {
	srv, st, _ := newTestServer(t)
	actor := mustCreateSession(t, srv.sessions, 1, "admin", model.RoleAdmin)
	if err := st.(*datastore.ProviderFactory).Close(); err != nil {
		t.Fatal(err)
	}
	conn := &bufferConn{}
	srv.handleExportData(actor.ID, &pb.ExportDataRequest{Type: "channels"}, st, conn)
	response, err := protocol.ReadControlMessage(conn)
	if err != nil || response.ErrorResponse == nil || response.ErrorResponse.Message != "export failed" {
		t.Fatal("export error was not generic", err)
	}
}

func TestScreenStartHidesCipherInternals(t *testing.T) {
	srv, _, handler := newTestServerWithConfig(t, func(cfg *Config) { cfg.EnableScreenShare = true; cfg.MediaCipher = "internal-invalid-suite" })
	session := mustCreateSession(t, srv.sessions, 1, "sharer", model.RoleUser)
	srv.sessions.SetChannel(session.ID, 1)
	conn := &bufferConn{}
	srv.handleScreenShareStart(handler, session.ID, &pb.ScreenShareStartRequest{Width: 100, Height: 100}, conn)
	response, err := protocol.ReadControlMessage(conn)
	if err != nil || response.ErrorResponse == nil || response.ErrorResponse.Message != "screen share failed" {
		t.Fatalf("response=%#v err=%v", response, err)
	}
}
