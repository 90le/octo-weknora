package wire

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"testing"
)

func TestFraming(t *testing.T) {
	packet := frame(0x50, bytes.Repeat([]byte{42}, 200))
	for i := 0; i < len(packet); i++ {
		_, _, n, err := Next(packet[:i])
		if err != nil || n != 0 {
			t.Fatalf("fragment %d: %d %v", i, n, err)
		}
	}
	h, b, n, err := Next(append(packet, 0x80))
	if err != nil || h != 0x50 || n != len(packet) || len(b) != 200 {
		t.Fatal("concatenated frame")
	}
	_, _, _, err = Next([]byte{0x50, 255, 255, 255, 255, 0})
	if err == nil {
		t.Fatal("unbounded length accepted")
	}
	_, _, n, err = Next([]byte{0x80})
	if err != nil || n != 1 {
		t.Fatal("single-byte pong")
	}
}

func TestHandshakeEncryptedReceiveAndAck(t *testing.T) {
	s, err := NewSession()
	if err != nil {
		t.Fatal(err)
	}
	connect, err := s.Connect("robot", "secret", "device", 123)
	if err != nil {
		t.Fatal(err)
	}
	_, body, _, _ := Next(connect)
	d := &decoder{r: bytes.NewReader(body)}
	if d.u8() != 4 || d.u8() != 0 || d.str() != "device" || d.str() != "robot" || d.str() != "secret" || d.u64() != 123 {
		t.Fatal("connect fields")
	}
	clientPublic, err := base64.StdEncoding.DecodeString(d.str())
	if err != nil {
		t.Fatal(err)
	}
	server, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ecdh.X25519().NewPublicKey(clientPublic)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := server.ECDH(pub)
	if err != nil {
		t.Fatal(err)
	}
	hash := md5.Sum([]byte(base64.StdEncoding.EncodeToString(secret)))
	key := []byte(hex.EncodeToString(hash[:])[:16])
	iv := []byte("1234567890123456")
	ack := &bytes.Buffer{}
	ack.WriteByte(4)
	_ = binary.Write(ack, binary.BigEndian, uint64(0))
	ack.WriteByte(1)
	_ = putString(ack, base64.StdEncoding.EncodeToString(server.PublicKey().Bytes()))
	_ = putString(ack, string(iv))
	_ = binary.Write(ack, binary.BigEndian, uint64(7))
	if err = s.Accept(0x21, ack.Bytes()); err != nil {
		t.Fatal(err)
	}
	if s.Accept(0x21, ack.Bytes()) == nil {
		t.Fatal("duplicate handshake accepted")
	}
	plain := []byte(`{"type":1,"content":"你好"}`)
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	plain = append(plain, bytes.Repeat([]byte{byte(pad)}, pad)...)
	block, _ := aes.NewCipher(key)
	ciphertext := make([]byte, len(plain))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(ciphertext, plain)
	recv := &bytes.Buffer{}
	recv.WriteByte(0)
	_ = putString(recv, "key")
	_ = putString(recv, "user")
	_ = putString(recv, "group____2098355867442221056")
	recv.WriteByte(5)
	_ = binary.Write(recv, binary.BigEndian, uint32(0))
	_ = putString(recv, "client")
	_ = binary.Write(recv, binary.BigEndian, uint64(18446744073709551614))
	_ = binary.Write(recv, binary.BigEndian, uint32(25))
	_ = binary.Write(recv, binary.BigEndian, uint32(1234))
	recv.WriteString(base64.StdEncoding.EncodeToString(ciphertext))
	m, err := s.Receive(recv.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "18446744073709551614" || m.ChannelType != 5 || m.Sender != "user" || !bytes.Contains(m.Payload, []byte("你好")) {
		t.Fatalf("message: %+v", m)
	}
	encoded, err := Acknowledge(m)
	if err != nil {
		t.Fatal(err)
	}
	_, ackBody, _, _ := Next(encoded)
	if binary.BigEndian.Uint64(ackBody) != 18446744073709551614 {
		t.Fatal("lost int64 precision")
	}
	if _, err = s.Receive(recv.Bytes()[:15]); err == nil {
		t.Fatal("truncated receive accepted")
	}
	if _, err = (&Session{}).Receive(recv.Bytes()); err == nil {
		t.Fatal("unauthenticated receive accepted")
	}
	transient := append([]byte(nil), recv.Bytes()...)
	transient[0] = 2
	stream, err := s.Receive(transient)
	if err != nil || !stream.Stream {
		t.Fatal("stream signal stopped receive decoding")
	}
	if _, err = Acknowledge(&Message{ID: "0", Sequence: 0}); err != nil {
		t.Fatal("transient ACK rejected")
	}
}

func FuzzFrame(f *testing.F) {
	f.Add([]byte{0x80})
	f.Add(frame(0x50, []byte("test")))
	f.Fuzz(func(t *testing.T, b []byte) {
		_, _, n, _ := Next(b)
		if n < 0 || n > len(b) {
			t.Fatal("invalid consumed length")
		}
	})
}
