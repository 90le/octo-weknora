// Package wire implements Octo's WuKongIM v4 transport without an Agent runtime.
// Protocol reference: Mininglamp-OSS/openclaw-channel-octo src/socket.ts at
// 6b5b3f14457df72ab95d2159ad50214faed793e4 (Apache-2.0).
package wire

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/md5" // Required by the upstream wire protocol's key derivation.
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"unicode/utf8"
)

const MaxPacket = 2 << 20

var ErrProtocol = errors.New("invalid Octo transport packet")

// A server kick must stop the runner, not compete with another Bot connection.
var ErrDisconnected = errors.New("Octo disconnected by server")

// Message identifiers stay decimal strings, including values beyond JS's 2^53.
type Message struct {
	ID          string
	Sequence    uint32
	Sender      string
	Channel     string
	ChannelType byte
	Timestamp   uint32
	Payload     json.RawMessage
}

type decoder struct {
	r   *bytes.Reader
	err error
}

func (d *decoder) number(v any) {
	if d.err == nil {
		d.err = binary.Read(d.r, binary.BigEndian, v)
	}
}
func (d *decoder) u8() (v byte)    { d.number(&v); return }
func (d *decoder) u32() (v uint32) { d.number(&v); return }
func (d *decoder) u64() (v uint64) { d.number(&v); return }
func (d *decoder) str() string {
	var n uint16
	d.number(&n)
	if d.err != nil {
		return ""
	}
	b := make([]byte, int(n))
	_, d.err = io.ReadFull(d.r, b)
	if !utf8.Valid(b) {
		d.err = ErrProtocol
	}
	return string(b)
}

func putString(b *bytes.Buffer, s string) error {
	if len(s) > 65535 || !utf8.ValidString(s) {
		return ErrProtocol
	}
	_ = binary.Write(b, binary.BigEndian, uint16(len(s)))
	b.WriteString(s)
	return nil
}

// Next handles both concatenated packets and partial frames. A zero consumed
// count means more bytes are needed; an invalid/oversized length is fatal.
func Next(data []byte) (header byte, body []byte, consumed int, err error) {
	if len(data) == 0 {
		return
	}
	header = data[0]
	if header>>4 == 7 || header>>4 == 8 {
		return header, nil, 1, nil
	}
	n := 0
	for i := 1; i <= 4; i++ {
		if i >= len(data) {
			return header, nil, 0, nil
		}
		n |= int(data[i]&127) << (7 * (i - 1))
		if n > MaxPacket {
			return 0, nil, 0, ErrProtocol
		}
		if data[i]&128 == 0 {
			if len(data) < i+1+n {
				return header, nil, 0, nil
			}
			return header, data[i+1 : i+1+n], i + 1 + n, nil
		}
	}
	return 0, nil, 0, ErrProtocol
}

func frame(header byte, body []byte) []byte {
	out := []byte{header}
	n := len(body)
	for {
		v := byte(n & 127)
		n >>= 7
		if n != 0 {
			v |= 128
		}
		out = append(out, v)
		if n == 0 {
			break
		}
	}
	return append(out, body...)
}

// Session crypto state belongs to one connection and is never reused on reconnect.
type Session struct {
	private *ecdh.PrivateKey
	key, iv []byte
	version byte
	ready   bool
}

func NewSession() (*Session, error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return &Session{private: k}, nil
}

func (s *Session) Connect(uid, token, device string, timestamp int64) ([]byte, error) {
	b := &bytes.Buffer{}
	b.Write([]byte{4, 0})
	for _, v := range []string{device, uid, token} {
		if err := putString(b, v); err != nil {
			return nil, err
		}
	}
	_ = binary.Write(b, binary.BigEndian, timestamp)
	if err := putString(b, base64.StdEncoding.EncodeToString(s.private.PublicKey().Bytes())); err != nil {
		return nil, err
	}
	return frame(0x10, b.Bytes()), nil
}

func (s *Session) Accept(header byte, body []byte) error {
	if s.ready || header>>4 != 2 {
		return ErrProtocol
	}
	d := &decoder{r: bytes.NewReader(body)}
	if header&1 != 0 {
		s.version = d.u8()
	}
	_ = d.u64()
	reason := d.u8()
	serverKey, salt := d.str(), d.str()
	if s.version >= 4 {
		_ = d.u64()
	}
	if d.err != nil || reason != 1 || d.r.Len() != 0 || len(salt) < 16 {
		return ErrProtocol
	}
	raw, err := base64.StdEncoding.DecodeString(serverKey)
	if err != nil {
		return ErrProtocol
	}
	pub, err := ecdh.X25519().NewPublicKey(raw)
	if err != nil {
		return ErrProtocol
	}
	secret, err := s.private.ECDH(pub)
	if err != nil {
		return ErrProtocol
	}
	hash := md5.Sum([]byte(base64.StdEncoding.EncodeToString(secret)))
	s.key = []byte(hex.EncodeToString(hash[:])[:16])
	s.iv = []byte(salt[:16])
	s.ready = true
	return nil
}

func (s *Session) Receive(body []byte) (*Message, error) {
	if !s.ready {
		return nil, ErrProtocol
	}
	d := &decoder{r: bytes.NewReader(body)}
	setting := d.u8()
	// Streaming RECV packets have an additional layout. Do not interpret them as ordinary input.
	if setting&2 != 0 {
		return nil, ErrProtocol
	}
	_ = d.str() // message key
	m := &Message{Sender: d.str(), Channel: d.str(), ChannelType: d.u8()}
	if s.version >= 3 {
		_ = d.u32()
	}
	_ = d.str() // client message number
	m.ID = strconv.FormatUint(d.u64(), 10)
	m.Sequence, m.Timestamp = d.u32(), d.u32()
	if setting&8 != 0 {
		_ = d.str()
	}
	if d.err != nil || m.Sender == "" || m.Channel == "" || m.ID == "0" {
		return nil, ErrProtocol
	}
	encoded, err := io.ReadAll(d.r)
	if err != nil {
		return nil, ErrProtocol
	}
	encrypted, err := base64.StdEncoding.DecodeString(string(encoded))
	if err != nil || len(encrypted) == 0 || len(encrypted)%aes.BlockSize != 0 {
		return nil, ErrProtocol
	}
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, ErrProtocol
	}
	plain := make([]byte, len(encrypted))
	cipher.NewCBCDecrypter(block, s.iv).CryptBlocks(plain, encrypted)
	padding := int(plain[len(plain)-1])
	if padding < 1 || padding > aes.BlockSize || padding > len(plain) {
		return nil, ErrProtocol
	}
	for _, v := range plain[len(plain)-padding:] {
		if int(v) != padding {
			return nil, ErrProtocol
		}
	}
	plain = plain[:len(plain)-padding]
	if !utf8.Valid(plain) || !json.Valid(plain) || len(plain) == 0 || plain[0] != '{' {
		return nil, ErrProtocol
	}
	m.Payload = json.RawMessage(plain)
	return m, nil
}

func Acknowledge(m *Message) ([]byte, error) {
	id, err := strconv.ParseUint(m.ID, 10, 64)
	if err != nil || id == 0 {
		return nil, fmt.Errorf("%w: message id", ErrProtocol)
	}
	b := &bytes.Buffer{}
	_ = binary.Write(b, binary.BigEndian, id)
	_ = binary.Write(b, binary.BigEndian, m.Sequence)
	return frame(0x60, b.Bytes()), nil
}
