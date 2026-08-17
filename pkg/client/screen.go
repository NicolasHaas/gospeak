package client

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/protocol"
)

type ScreenPacketHandler func(pkt *protocol.ScreenPacket)

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
	tlsCfg, err := tlsConfig(addr, serverIdentity)
	if err != nil {
		return nil, err
	}

	dialer := &tls.Dialer{Config: tlsCfg}
	dialCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	conn, err := dialer.DialContext(dialCtx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("client: connect screen: %w", err)
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
