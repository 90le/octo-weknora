package wire

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Credentials is obtained from the authenticated Octo register API, not a message.
type Credentials struct{ UID, Token, URL string }

// RunConnection runs one authenticated connection. The caller owns reconnect and
// registration refresh. Dispatch must durably accept/queue a message before it
// returns nil. An error closes the connection without acknowledging that message.
// This layer does not execute an Agent, decide KB access, or log message bodies.
func RunConnection(ctx context.Context, creds Credentials, dispatch func(context.Context, *Message) error) error {
	if dispatch == nil || creds.UID == "" || creds.Token == "" {
		return errors.New("Octo connection requires identity and dispatch")
	}
	u, err := url.Parse(creds.URL)
	if err != nil || u.Scheme != "wss" || u.Hostname() != "im.deepminer.com.cn" || (u.Port() != "" && u.Port() != "443") || u.User != nil || u.Fragment != "" {
		return errors.New("untrusted Octo WebSocket URL")
	}
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second, Proxy: http.ProxyFromEnvironment}
	conn, resp, err := dialer.DialContext(ctx, creds.URL, nil)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		return errors.New("Octo WebSocket connection failed")
	}
	defer conn.Close()
	conn.SetReadLimit(MaxPacket)
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	var writes sync.Mutex
	write := func(data []byte) error {
		writes.Lock()
		defer writes.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return conn.WriteMessage(websocket.BinaryMessage, data)
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	s, err := NewSession()
	if err != nil {
		return err
	}
	device := make([]byte, 16)
	if _, err = rand.Read(device); err != nil {
		return err
	}
	packet, err := s.Connect(creds.UID, creds.Token, hex.EncodeToString(device)+"W", time.Now().UnixMilli())
	if err != nil {
		return err
	}
	if err = write(packet); err != nil {
		return errors.New("Octo connect write failed")
	}
	buffer := []byte{}
	ready := false
	for {
		kind, data, readErr := conn.ReadMessage()
		if readErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errors.New("Octo connection closed")
		}
		if kind != websocket.BinaryMessage {
			return ErrProtocol
		}
		if len(buffer)+len(data) > MaxPacket+5 {
			return ErrProtocol
		}
		buffer = append(buffer, data...)
		for len(buffer) > 0 {
			header, body, n, parseErr := Next(buffer)
			if parseErr != nil {
				return parseErr
			}
			if n == 0 {
				break
			}
			switch header >> 4 {
			case 2:
				if err = s.Accept(header, body); err != nil {
					return err
				}
				ready = true
				_ = conn.SetReadDeadline(time.Now().Add(150 * time.Second))
				go func() {
					timer := time.NewTicker(60 * time.Second)
					defer timer.Stop()
					for {
						select {
						case <-done:
							return
						case <-ctx.Done():
							return
						case <-timer.C:
							if write([]byte{0x70}) != nil {
								_ = conn.Close()
								return
							}
						}
					}
				}()
			case 8:
				if !ready {
					return ErrProtocol
				}
				_ = conn.SetReadDeadline(time.Now().Add(150 * time.Second))
			case 7:
				if err = write([]byte{0x80}); err != nil {
					return errors.New("Octo pong failed")
				}
			case 5:
				message, recvErr := s.Receive(body)
				if recvErr != nil {
					return recvErr
				}
				if err = dispatch(ctx, message); err != nil {
					return errors.New("Octo inbound was not accepted")
				}
				ack, ackErr := Acknowledge(message)
				if ackErr != nil {
					return ackErr
				}
				if err = write(ack); err != nil {
					return errors.New("Octo acknowledgment failed")
				}
			case 9:
				return ErrDisconnected
			default:
				if !ready {
					return ErrProtocol
				}
			}
			buffer = buffer[n:]
		}
	}
}
