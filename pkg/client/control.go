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

	"github.com/NicolasHaas/gospeak/pkg/protocol"
	pb "github.com/NicolasHaas/gospeak/pkg/protocol/pb"
)

// EventHandler is a callback for incoming control events.
type EventHandler func(msg *pb.ControlMessage)

// ControlClient manages the TCP/TLS control plane connection.
type ControlClient struct {
	conn    net.Conn
	mu      sync.Mutex
	handler EventHandler
	done    chan struct{}
}

// NewControlClient connects to the server's control plane via TLS.
func NewControlClient(addr string) (*ControlClient, error) {
	tlsCfg := &tls.Config{
		InsecureSkipVerify: true, // MVP: accept self-signed certs (TOFU model)
		MinVersion:         tls.VersionTLS13,
	}

	dialer := &tls.Dialer{Config: tlsCfg}
	conn, err := dialer.DialContext(context.Background(), "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("client: connect control: %w", err)
	}

	return &ControlClient{
		conn: conn,
		done: make(chan struct{}),
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
