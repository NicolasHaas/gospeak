package client

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/protocol"
)

type ScreenPacketHandler func(pkt *protocol.ScreenPacket)

// screenTransportUnavailable is emitted only before TLS or authentication starts.
type screenTransportUnavailable struct{ error }

func (e *screenTransportUnavailable) Unwrap() error { return e.error }

func screenDialUnavailable(err error) bool {
	var dns *net.DNSError
	if errors.As(err, &dns) || errors.Is(err, context.Canceled) {
		return false
	}
	var op *net.OpError
	if !errors.As(err, &op) || op.Op != "dial" {
		return false
	}
	return op.Timeout() || errors.Is(err, screenConnectionRefused) ||
		errors.Is(err, screenNetworkUnreachable) || errors.Is(err, screenHostUnreachable)
}

// ScreenClient manages the TCP/TLS screen-share media connection.
type ScreenClient struct {
	conn    net.Conn
	mu      sync.Mutex
	handler ScreenPacketHandler
	done    chan struct{}
}

func NewScreenClient(addr string, sessionID uint32, authToken, serverIdentity string) (*ScreenClient, error) {
	return NewScreenClientContext(context.Background(), addr, sessionID, authToken, serverIdentity)
}

func NewScreenClientContext(ctx context.Context, addr string, sessionID uint32, authToken, serverIdentity string) (*ScreenClient, error) {
	return newScreenClientContextWithTrust(ctx, addr, sessionID, authToken, tlsTrustPolicyForPin(serverIdentity))
}

func newScreenClientContextWithTrust(ctx context.Context, addr string, sessionID uint32, authToken string, trust tlsTrustPolicy) (*ScreenClient, error) {
	// Validate advertised credentials even if the optional transport is down.
	if err := protocol.WriteScreenAuth(io.Discard, &protocol.ScreenAuth{SessionID: sessionID, Token: authToken}); err != nil {
		return nil, err
	}
	tlsCfg, err := newTLSConfigWithTrust(addr, trust, nil)
	if err != nil {
		return nil, err
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	portNumber, err := net.DefaultResolver.LookupPort(ctx, "tcp", port)
	if err != nil || portNumber == 0 {
		return nil, fmt.Errorf("client: invalid screen port %q", port)
	}

	dialer := &net.Dialer{}
	dialCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	raw, err := dialer.DialContext(dialCtx, "tcp", addr)
	if err != nil {
		if ctx.Err() == nil && screenDialUnavailable(err) {
			return nil, &screenTransportUnavailable{err}
		}
		return nil, fmt.Errorf("client: connect screen: %w", err)
	}
	conn := tls.Client(raw, tlsCfg)
	if err := conn.HandshakeContext(dialCtx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("client: screen TLS handshake: %w", err)
	}
	stopCancel := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopCancel()
	if err := conn.SetWriteDeadline(time.Now().Add(connectTimeout)); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("client: set screen auth deadline: %w", err)
	}
	if err := protocol.WriteScreenAuth(conn, &protocol.ScreenAuth{SessionID: sessionID, Token: authToken}); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("client: authenticate screen: %w", err)
	}
	if err := conn.SetWriteDeadline(time.Time{}); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("client: clear screen auth deadline: %w", err)
	}

	return &ScreenClient{conn: conn, done: make(chan struct{})}, nil
}

func (c *ScreenClient) SetPacketHandler(handler ScreenPacketHandler) {
	c.handler = handler
}

func (c *ScreenClient) Send(pkt *protocol.ScreenPacket) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.conn.SetWriteDeadline(time.Now().Add(connectTimeout)); err != nil {
		return fmt.Errorf("client: set screen write deadline: %w", err)
	}
	writeErr := protocol.WriteScreenPacket(c.conn, pkt)
	clearErr := c.conn.SetWriteDeadline(time.Time{})
	if writeErr != nil {
		return writeErr
	}
	if clearErr != nil {
		return fmt.Errorf("client: clear screen write deadline: %w", clearErr)
	}
	return nil
}

func (c *ScreenClient) StartReceiving() {
	go func() {
		defer close(c.done)
		for {
			pkt, err := protocol.ReadScreenPacket(c.conn)
			if err != nil {
				if err == io.EOF || isClosedErr(err) {
					slog.Debug("screen connection closed")
					return
				}
				slog.Error("screen read error", "err", err)
				return
			}
			if c.handler != nil {
				c.handler(pkt)
			}
		}
	}()
}

func (c *ScreenClient) Close() error {
	return c.conn.Close()
}

func (c *ScreenClient) Done() <-chan struct{} {
	return c.done
}
