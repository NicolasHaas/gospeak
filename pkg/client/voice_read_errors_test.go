package client

import (
	"net"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/NicolasHaas/gospeak/pkg/protocol"
)

type voiceReadErrorConn struct {
	net.Conn
	err error
}

func (c *voiceReadErrorConn) Read(buf []byte) (int, error) {
	if c.err != nil {
		err := c.err
		c.err = nil
		return 0, err
	}
	return c.Conn.Read(buf)
}

func TestVoiceReceiverRecoversFromConnectionRefused(t *testing.T) {
	local, peer := net.Pipe()
	defer peer.Close()
	v := &VoiceClient{conn: &voiceReadErrorConn{Conn: local, err: &net.OpError{Op: "read", Net: "udp", Err: os.NewSyscallError("recvfrom", syscall.ECONNREFUSED)}}, IncomingPackets: make(chan *protocol.VoicePacket, 1), done: make(chan struct{})}
	defer v.Close()
	v.StartReceiving()
	sent := make(chan error, 1)
	go func() {
		_, err := peer.Write((&protocol.VoicePacket{SessionID: 123, SeqNum: 1, Payload: []byte("frame")}).Marshal())
		sent <- err
	}()
	select {
	case pkt := <-v.IncomingPackets:
		if pkt.SessionID != 123 {
			t.Fatal("wrong recovered packet")
		}
	case <-v.done:
		t.Fatal("transient read error terminated receiver")
	case <-time.After(time.Second):
		t.Fatal("receiver did not recover")
	}
	if err := <-sent; err != nil {
		t.Fatal(err)
	}
	if err := v.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-v.done:
	case <-time.After(time.Second):
		t.Fatal("closed receiver did not exit")
	}
}

func TestVoiceReceiverExitsOnPermanentError(t *testing.T) {
	for _, err := range []error{net.ErrClosed, syscall.EACCES, os.ErrDeadlineExceeded} {
		local, peer := net.Pipe()
		v := &VoiceClient{conn: &voiceReadErrorConn{Conn: local, err: err}, done: make(chan struct{})}
		v.StartReceiving()
		select {
		case <-v.done:
		case <-time.After(time.Second):
			t.Error("permanent error retried")
		}
		_ = v.Close()
		_ = peer.Close()
	}
}
