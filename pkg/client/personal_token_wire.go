package client

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/protocol"
	"github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

// VerifiedServerIdentity is the public-key identity actually checked by TLS.
func (c *ControlClient) VerifiedServerIdentity() string { return c.serverTrust.pin }

func PersonalCredentialRejected(err error) bool {
	var rejected *AuthenticationError
	return errors.As(err, &rejected) && rejected.Code == 2
}

// PersonalCredentialAdmissionRefused identifies the existing wire refusal before
// a dedicated credential request can have been sent. Code 3 alone is not enough.
func PersonalCredentialAdmissionRefused(err error) bool {
	var refused *AuthenticationError
	return errors.As(err, &refused) && refused.Code == 3 && refused.Message == "server session capacity reached"
}

// PersonalTokenExchange owns a dedicated control connection: do not combine it
// with StartReceiving. Its completion is independent of media generation cancel.
func (c *ControlClient) PersonalTokenExchange(ctx context.Context, request *pb.ControlMessage) (*pb.ControlMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	deadline := time.Now().Add(connectTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := c.conn.SetDeadline(deadline); err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	defer func() { _ = c.conn.SetDeadline(time.Time{}) }()
	if err := protocol.WriteControlMessage(c.conn, request); err != nil {
		return nil, err
	}
	for {
		response, err := protocol.ReadControlMessage(c.conn)
		if err != nil {
			return nil, err
		}
		var operationError string
		switch {
		case request.PersonalTokenStatusReq != nil && response.PersonalTokenStatusResp != nil:
			operationError = response.PersonalTokenStatusResp.Error
		case request.RotatePersonalTokenReq != nil && response.RotatePersonalTokenResp != nil:
			operationError = response.RotatePersonalTokenResp.Error
		case request.RevokePersonalTokenReq != nil && response.RevokePersonalTokenResp != nil:
			operationError = response.RevokePersonalTokenResp.Error
		case request.IssuePersonalRecoveryReq != nil && response.IssuePersonalRecoveryResp != nil:
			operationError = response.IssuePersonalRecoveryResp.Error
		case request.RevokePersonalRecoveryReq != nil && response.RevokePersonalRecoveryResp != nil:
			operationError = response.RevokePersonalRecoveryResp.Error
		case request.RedeemPersonalRecoveryReq != nil && response.RedeemPersonalRecoveryResp != nil:
			operationError = response.RedeemPersonalRecoveryResp.Error
		case response.ErrorResponse != nil:
			return nil, fmt.Errorf("credential request refused")
		default:
			// A new authenticated session may receive its initial roster repair first.
			if response.ServerStateEvent != nil {
				continue
			}
			return nil, fmt.Errorf("unexpected credential response")
		}
		if operationError != "" {
			return nil, fmt.Errorf("%s", operationError)
		}
		return response, nil
	}
}

// OpenPersonalTokenControl authenticates without creating an account. An invite
// or bootstrap exchange must never be mistaken for a personal credential.
func OpenPersonalTokenControl(ctx context.Context, addr, pin, username, token string) (*ControlClient, *pb.AuthResponse, error) {
	if token == "" || username == "" {
		return nil, nil, fmt.Errorf("current personal credential required")
	}
	// Local socket/engine teardown does not acknowledge server slot retirement.
	// Retry only a definitive admission refusal, before any operation is sent.
	// Never retry transport errors or PersonalTokenExchange (unknown outcome).
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	var admissionErr error
	for {
		if ctx.Err() != nil && admissionErr != nil {
			return nil, nil, admissionErr
		}
		c, err := NewControlClientContext(ctx, addr, pin)
		if err != nil {
			if ctx.Err() != nil && admissionErr != nil {
				return nil, nil, admissionErr
			}
			return nil, nil, err
		}
		stop := context.AfterFunc(ctx, func() { _ = c.Close() })
		auth, err := c.authenticate(token, username, true)
		stop()
		if err == nil {
			if ctx.Err() != nil {
				_ = c.Close()
				return nil, nil, ctx.Err()
			}
			if auth.UserID <= 0 || auth.Username != username || auth.AutoToken != "" {
				_ = c.Close()
				return nil, nil, fmt.Errorf("current personal credential required")
			}
			return c, auth, nil
		}
		_ = c.Close()
		if !PersonalCredentialAdmissionRefused(err) {
			return nil, nil, err
		}
		admissionErr = err
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, nil, err // retain the typed, known pre-mutation refusal
		case <-timer.C:
		}
	}
}

// SubmitPersonalCredential uses only a disk-authoritative staged attempt. Any
// response loss leaves exactly that candidate available for reconciliation.
func (bs *BookmarkStore) SubmitPersonalCredential(ctx context.Context, p PendingPersonalCredential) error {
	saved, found, err := bs.PendingPersonalCredential(p.ControlAddr, p.Username, p.AccountID)
	if err != nil {
		return err
	}
	if !found || saved != p {
		return fmt.Errorf("credential attempt is not current")
	}
	var c *ControlClient
	var request *pb.ControlMessage
	if p.RecoveryGrant != "" {
		c, err = NewControlClientContext(ctx, p.ControlAddr, p.ServerIdentity)
		request = &pb.ControlMessage{RedeemPersonalRecoveryReq: &pb.RedeemPersonalRecoveryRequest{TargetID: p.AccountID, Username: p.Username, Grant: p.RecoveryGrant, Replacement: p.Candidate}}
	} else {
		var auth *pb.AuthResponse
		c, auth, err = OpenPersonalTokenControl(ctx, p.ControlAddr, p.ServerIdentity, p.Username, p.OldToken)
		if err == nil && (auth.UserID != p.AccountID || auth.CredentialVersion != p.Version) {
			_ = c.Close()
			return fmt.Errorf("credential changed; retain the saved candidate")
		}
		request = &pb.ControlMessage{RotatePersonalTokenReq: &pb.RotatePersonalTokenRequest{Version: p.Version, CurrentToken: p.OldToken, Replacement: p.Candidate}}
	}
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = c.PersonalTokenExchange(ctx, request)
	return err
}

// VerifyPersonalCredential performs no local write and never tries a different
// token. Only successful exact account authentication proves the saved candidate.
func VerifyPersonalCredential(ctx context.Context, p PendingPersonalCredential) error {
	if !p.valid() {
		return fmt.Errorf("invalid pending credential")
	}
	c, auth, err := OpenPersonalTokenControl(ctx, p.ControlAddr, p.ServerIdentity, p.Username, p.Candidate)
	if err != nil {
		return err
	}
	defer c.Close()
	if auth.UserID != p.AccountID {
		return fmt.Errorf("candidate account identity mismatch")
	}
	return nil
}
