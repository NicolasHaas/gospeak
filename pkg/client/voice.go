package client

import (
	"fmt"
	"log/slog"
	"net"
	"sync"

	gospeakCrypto "github.com/NicolasHaas/gospeak/pkg/crypto"
	"github.com/NicolasHaas/gospeak/pkg/protocol"
)

// VoiceClient manages the UDP voice connection.
type VoiceClient struct {
	conn       *net.UDPConn
	serverAddr *net.UDPAddr
	sessionID  uint32
	channelID  uint16
	cipher     *gospeakCrypto.VoiceCipher
	seqNum     uint32
	mu         sync.Mutex

	// Incoming voice packets are sent here
	IncomingPackets chan *protocol.VoicePacket

	done chan struct{}
}

// NewVoiceClient creates a new UDP voice client.
func NewVoiceClient(serverAddr string, sessionID uint32, encKey []byte) (*VoiceClient, error) {
	addr, err := net.ResolveUDPAddr("udp", serverAddr)
	if err != nil {
		return nil, fmt.Errorf("client: resolve voice addr: %w", err)
	}

	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return nil, fmt.Errorf("client: dial voice: %w", err)
	}

	cipher, err := gospeakCrypto.NewVoiceCipher(encKey)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("client: voice cipher: %w", err)
	}

	// Increase buffer sizes
	_ = conn.SetReadBuffer(512 * 1024)
	_ = conn.SetWriteBuffer(512 * 1024)

	return &VoiceClient{
		conn:            conn,
		serverAddr:      addr,
		sessionID:       sessionID,
		cipher:          cipher,
		IncomingPackets: make(chan *protocol.VoicePacket, 100),
		done:            make(chan struct{}),
	}, nil
}

// SetChannel sets the current channel ID for outgoing packets.
func (v *VoiceClient) SetChannel(channelID int64) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.channelID = uint16(channelID) //nolint:gosec // channel IDs fit in uint16
}

// SendVoice encrypts and sends an Opus frame over UDP.
func (v *VoiceClient) SendVoice(opusData []byte, timestamp uint32) error {
	v.mu.Lock()
	v.seqNum++
	seqNum := v.seqNum
	channelID := v.channelID
	v.mu.Unlock()

	pkt := &protocol.VoicePacket{
		SessionID: v.sessionID,
		SeqNum:    seqNum,
		Timestamp: timestamp,
		ChannelID: channelID,
	}

	header := pkt.MarshalHeader()
	pkt.Payload = v.cipher.Encrypt(v.sessionID, seqNum, header, opusData)

	_, err := v.conn.Write(pkt.Marshal())
	return err
}

// StartReceiving starts listening for incoming voice packets.
func (v *VoiceClient) StartReceiving() {
	go func() {
		defer close(v.done)
		buf := make([]byte, protocol.VoiceHeaderSize+protocol.MaxVoicePayload)

		for {
			n, err := v.conn.Read(buf)
			if err != nil {
				select {
				case <-v.done:
					return
				default:
					slog.Debug("voice read error", "err", err)
					return
				}
			}

			pkt, err := protocol.UnmarshalVoicePacket(buf[:n])
			if err != nil {
				continue
			}

			select {
			case v.IncomingPackets <- pkt:
			default:
				// Drop packet if channel is full (back-pressure)
			}
		}
	}()
}

// Close closes the voice connection.
func (v *VoiceClient) Close() error {
	return v.conn.Close()
}
