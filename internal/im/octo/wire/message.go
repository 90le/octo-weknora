package wire

import (
	"encoding/json"
	"errors"
	"strings"
)

type Entity struct {
	UID    string `json:"uid"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
}
type Mention struct {
	UIDs     []string `json:"uids"`
	Entities []Entity `json:"entities"`
}
type Payload struct {
	Type    int             `json:"type"`
	Content json.RawMessage `json:"content"`
	Plain   string          `json:"plain"`
	URL     string          `json:"url"`
	Name    string          `json:"name"`
	Size    int64           `json:"size"`
	Mention Mention         `json:"mention"`
	Reply   *struct {
		ID      json.RawMessage `json:"message_id"`
		Sender  string          `json:"from_uid"`
		Name    string          `json:"from_name"`
		Payload *Payload        `json:"payload"`
	} `json:"reply"`
}

func (p *Payload) Text() string {
	if p == nil {
		return ""
	}
	if p.Type == 14 {
		return p.Plain
	}
	var text string
	_ = json.Unmarshal(p.Content, &text)
	return text
}

// Scope retains the parent group and subarea separately. Channel remains the
// native routing key and must also partition per-user conversational sessions.
type Scope struct {
	Channel        string
	Type           byte
	Group, Subarea string
}

func ParseScope(channel string, kind byte) (Scope, error) {
	s := Scope{Channel: channel, Type: kind}
	if channel == "" || len(channel) > 256 || strings.ContainsAny(channel, "/\\?#%\x00\r\n\t ") {
		return s, errors.New("invalid Octo channel")
	}
	switch kind {
	case 1, 2:
		if strings.Contains(channel, "____") {
			return s, errors.New("invalid Octo channel type")
		}
		if kind == 2 {
			s.Group = channel
		}
	case 5:
		parts := strings.Split(channel, "____")
		if len(parts) != 2 || parts[0] == "" || !DecimalID(parts[1]) {
			return s, errors.New("invalid Octo subarea")
		}
		s.Group, s.Subarea = parts[0], parts[1]
	default:
		return s, errors.New("unsupported Octo channel")
	}
	return s, nil
}

func DecimalID(id string) bool {
	if id == "" || len(id) > 20 {
		return false
	}
	for _, c := range id {
		if c < '0' || c > '9' {
			return false
		}
	}
	return id != "0"
}

func ReadID(raw json.RawMessage) string {
	var text string
	if len(raw) > 0 && raw[0] == '"' {
		if json.Unmarshal(raw, &text) != nil {
			return ""
		}
	} else {
		text = string(raw)
	}
	if !DecimalID(text) {
		return ""
	}
	return text
}

// AddressedTo reads native UID mentions only; display names and @all are not Bot identity.
func (p *Payload) AddressedTo(uid string) bool {
	for _, v := range p.Mention.UIDs {
		if v == uid {
			return true
		}
	}
	for _, v := range p.Mention.Entities {
		if v.UID == uid {
			return true
		}
	}
	return false
}
