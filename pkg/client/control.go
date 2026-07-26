// Package client implements the GoSpeak client networking.
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
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

// connectTimeout bounds the control-plane TCP+TLS handshake.
const connectTimeout = 3 * time.Second

// EventHandler is a callback for incoming control events.
type EventHandler func(msg *pb.ControlMessage)

// ControlClient manages the TCP/TLS control plane connection.
type ControlClient struct {
	conn           net.Conn
	serverIdentity string
	mu             sync.Mutex
	handler        EventHandler
	done           chan struct{}
}

// NewControlClient connects to the server's control plane via TLS.
func NewControlClient(addr, expectedPin string) (*ControlClient, error) {
	tlsCfg, err := tlsConfig(addr, expectedPin)
	if err != nil {
		return nil, err
	}

	dialer := &tls.Dialer{Config: tlsCfg}
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("client: connect control: %w", err)
	}
	tlsConn, ok := conn.(*tls.Conn)
	if !ok || len(tlsConn.ConnectionState().PeerCertificates) == 0 {
		_ = conn.Close()
		return nil, fmt.Errorf("client: control connection has no TLS peer identity")
	}

	return &ControlClient{
		conn:           conn,
		serverIdentity: SPKIFingerprint(tlsConn.ConnectionState().PeerCertificates[0]),
		done:           make(chan struct{}),
	}, nil
}

// SetEventHandler sets the callback for incoming control messages.
func (c *ControlClient) SetEventHandler(handler EventHandler) {
	c.handler = handler
}

// Send sends a control message to the server.
func (c *ControlClient) Send(msg *pb.ControlMessage) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return protocol.WriteControlMessage(c.conn, msg)
}

// Authenticate sends an auth request and returns the auth response.
func (c *ControlClient) Authenticate(token, username string) (*pb.AuthResponse, error) {
	if err := c.Send(&pb.ControlMessage{
		AuthRequest: &pb.AuthRequest{
			Token:    token,
			Username: username,
		},
	}); err != nil {
		return nil, fmt.Errorf("client: send auth: %w", err)
	}

	msg, err := protocol.ReadControlMessage(c.conn)
	if err != nil {
		return nil, fmt.Errorf("client: read auth response: %w", err)
	}

	if msg.ErrorResponse != nil {
		return nil, fmt.Errorf("auth failed: %s", msg.ErrorResponse.Message)
	}

	if msg.AuthResponse == nil {
		return nil, fmt.Errorf("client: unexpected response type")
	}

	return msg.AuthResponse, nil
}

// StartReceiving starts a goroutine that reads incoming control messages
// and dispatches them to the event handler.
func (c *ControlClient) StartReceiving() {
	go func() {
		defer close(c.done)
		for {
			msg, err := protocol.ReadControlMessage(c.conn)
			if err != nil {
				if err == io.EOF || isClosedErr(err) {
					slog.Debug("control connection closed")
					return
				}
				slog.Error("control read error", "err", err)
				return
			}
			if c.handler != nil {
				c.handler(msg)
			}
		}
	}()
}

// Close closes the control connection.
func (c *ControlClient) Close() error {
	return c.conn.Close()
}

// Done returns a channel that's closed when the connection is lost.
func (c *ControlClient) Done() <-chan struct{} {
	return c.done
}

func isClosedErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return s == "use of closed network connection" ||
		s == "tls: use of closed connection" ||
		s == "EOF"
}
