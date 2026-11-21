package server

import (
	"context"
	"github.com/NicolasHaas/gospeak/pkg/client"
	"github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/model"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
	"testing"
)

func TestPersonalCredentialCASLossFreshGrantRestart(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	srv, st, handler := newTestServer(t)
	admin, _ := personalMutationActor(t, srv, st, handler, "cas-admin", model.RoleAdmin, false)
	target, _ := personalMutationActor(t, srv, st, handler, "cas-user", model.RoleUser, false)
	addr, pin := personalCredentialListener(t, srv, handler, st, true)
	ctx := context.Background()
	old := "personal-" + target.Username
	c, auth, err := client.OpenPersonalTokenControl(ctx, addr, pin, target.Username, old)
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	candidate, _ := crypto.GenerateToken()
	attempt, _ := crypto.GenerateToken()
	p := client.PendingPersonalCredential{ControlAddr: addr, ServerIdentity: pin, Username: target.Username, AccountID: target.ID, OldToken: old, Candidate: candidate, Attempt: attempt, Version: auth.CredentialVersion}
	bs := client.NewBookmarkStore()
	if err := bs.Save(); err != nil {
		t.Fatal(err)
	}
	if err := bs.StagePersonalCredential(p, true); err != nil {
		t.Fatal(err)
	}
	// Another credential mutation wins after private staging and before submission.
	a, _, err := client.OpenPersonalTokenControl(ctx, addr, pin, admin.Username, "personal-"+admin.Username)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err := a.PersonalTokenExchange(ctx, &pb.ControlMessage{RevokePersonalTokenReq: &pb.RevokePersonalTokenRequest{TargetID: target.ID, Version: p.Version}}); err != nil {
		t.Fatal(err)
	}
	if err := bs.SubmitPersonalCredential(ctx, p); !client.PersonalCredentialRejected(err) {
		t.Fatalf("old credential was not definitively retired: %v", err)
	}
	if err := client.VerifyPersonalCredential(ctx, p); !client.PersonalCredentialRejected(err) {
		t.Fatalf("losing candidate authenticated: %v", err)
	}
	status, err := a.PersonalTokenExchange(ctx, &pb.ControlMessage{PersonalTokenStatusReq: &pb.PersonalTokenStatusRequest{TargetID: target.ID}})
	if err != nil {
		t.Fatal(err)
	}
	grant, err := a.PersonalTokenExchange(ctx, &pb.ControlMessage{IssuePersonalRecoveryReq: &pb.IssuePersonalRecoveryRequest{TargetID: target.ID, Version: status.PersonalTokenStatusResp.Version}})
	if err != nil {
		t.Fatal(err)
	}
	q, err := bs.RecoverPendingPersonalCredential(p, grant.IssuePersonalRecoveryResp.Grant, pin)
	if err != nil {
		t.Fatal(err)
	}
	if q.Candidate != p.Candidate || q.OldToken != p.OldToken || q.Attempt == p.Attempt {
		t.Fatal("recovery lost candidate or operation binding")
	}
	if err := bs.SubmitPersonalCredential(ctx, p); err == nil {
		t.Fatal("stale operation submitted")
	}
	// The production server commits; fixture drops only its success ACK.
	if err := bs.SubmitPersonalCredential(ctx, q); err == nil {
		t.Fatal("missing injected lost acknowledgement")
	}
	restarted := client.NewBookmarkStore()
	if err := restarted.Load(); err != nil {
		t.Fatal(err)
	}
	saved, found, err := restarted.PendingPersonalCredential(addr, target.Username, target.ID)
	if err != nil || !found || saved != q {
		t.Fatal("restart lost exact recovery")
	}
	if err := restarted.PromotePersonalCredential(p); err == nil {
		t.Fatal("stale completion cleared newer recovery")
	}
	if err := restarted.ReconcilePersonalCredential(ctx, saved); err != nil {
		t.Fatal(err)
	}
	if len(restarted.PendingCredentials) != 0 || restarted.Bookmarks[0].Token != p.Candidate {
		t.Fatal("verified candidate not promoted")
	}
	user, err := st.NonTx().GetUserByPersonalTokenHash(crypto.HashToken(candidate))
	if err != nil || user == nil || user.ID != target.ID || user.Role != target.Role || user.ChannelScope != target.ChannelScope {
		t.Fatal("recovery changed identity or authority")
	}
}
