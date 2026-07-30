package client

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"

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
	tlsCfg, err := tlsConfig(addr, serverIdentity)
	if err != nil {
		return nil, err
	}

	dialer := &tls.Dialer{Config: tlsCfg}
	conn, err := dialer.DialContext(context.Background(), "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("client: connect screen: %w", err)
	}
	if err := protocol.WriteScreenAuth(conn, &protocol.ScreenAuth{SessionID: sessionID, Token: authToken}); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("client: authenticate screen: %w", err)
	}

	return &ScreenClient{conn: conn, done: make(chan struct{})}, nil
}

func (c *ScreenClient) SetPacketHandler(handler ScreenPacketHandler) {
	c.handler = handler
}

func (c *ScreenClient) Send(pkt *protocol.ScreenPacket) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return protocol.WriteScreenPacket(c.conn, pkt)
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
