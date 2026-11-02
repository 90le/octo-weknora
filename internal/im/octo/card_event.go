package octo

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode"

	"github.com/Tencent/WeKnora/internal/im/octo/wire"
)

var errNotCardAction = errors.New("not a card action")

// cardAction carries only server-envelope identity and bounded form data.
// Its fields are not authorization; the caller must match a durable card
// binding and revalidate the operator's current scope before any side effect.
type cardAction struct {
	EventID     uint64
	MessageID   string
	ChannelID   string
	ChannelType byte
	ActionID    string
	OperatorUID string
	Inputs      map[string]string
}

type cardBinding struct {
	MessageID   string
	ChannelID   string
	ChannelType byte
	OperatorUID string
	Actions     map[string]bool
}

func (b cardBinding) matches(a cardAction) bool {
	return b.MessageID == a.MessageID && b.ChannelID == a.ChannelID &&
		b.ChannelType == a.ChannelType && b.OperatorUID == a.OperatorUID &&
		b.Actions[a.ActionID]
}

func validCardIdentifier(s string, limit int) bool {
	if s == "" || len(s) > limit {
		return false
	}
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validCardField(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || strings.ContainsRune("_.:-", r) {
			continue
		}
		return false
	}
	return true
}

func parseCardAction(raw []byte) (cardAction, error) {
	var out cardAction
	if len(raw) == 0 || len(raw) > 32<<10 {
		return out, errors.New("invalid card event size")
	}
	var envelope struct {
		ID   json.Number     `json:"event_id"`
		Type string          `json:"event_type"`
		Data json.RawMessage `json:"event_data"`
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if dec.Decode(&envelope) != nil {
		return out, errors.New("invalid card event")
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return out, errors.New("invalid card event")
	}
	if envelope.Type != "card_action" {
		return out, errNotCardAction
	}
	id, err := strconv.ParseUint(string(envelope.ID), 10, 63)
	if err != nil || id == 0 {
		return out, errors.New("invalid card event ID")
	}
	var data struct {
		MessageID   string                     `json:"message_id"`
		ChannelID   string                     `json:"channel_id"`
		ChannelType int                        `json:"channel_type"`
		ActionID    string                     `json:"action_id"`
		OperatorUID string                     `json:"operator_uid"`
		Inputs      map[string]json.RawMessage `json:"inputs"`
	}
	if json.Unmarshal(envelope.Data, &data) != nil || !wire.DecimalID(data.MessageID) ||
		!validCardIdentifier(data.OperatorUID, 128) || !validCardField(data.ActionID) ||
		(data.ChannelType != 1 && data.ChannelType != 2 && data.ChannelType != 5) {
		return out, errors.New("invalid card action identity")
	}
	if _, err := wire.ParseScope(data.ChannelID, byte(data.ChannelType)); err != nil {
		return out, errors.New("invalid card action scope")
	}
	if len(data.Inputs) > 32 {
		return out, errors.New("too many card inputs")
	}
	inputs := make(map[string]string, len(data.Inputs))
	total := 0
	for key, value := range data.Inputs {
		if !validCardField(key) {
			return out, errors.New("invalid card input name")
		}
		var text string
		if json.Unmarshal(value, &text) != nil {
			var scalar any
			d := json.NewDecoder(bytes.NewReader(value))
			d.UseNumber()
			if d.Decode(&scalar) != nil {
				return out, errors.New("invalid card input")
			}
			switch v := scalar.(type) {
			case bool:
				text = strconv.FormatBool(v)
			case json.Number:
				text = string(v)
			default:
				return out, errors.New("unsupported card input")
			}
		}
		total += len(key) + len(text)
		if len(text) > 4096 || total > 16384 {
			return out, errors.New("card inputs exceed size limit")
		}
		inputs[key] = text
	}
	return cardAction{
		EventID: id, MessageID: data.MessageID, ChannelID: data.ChannelID,
		ChannelType: byte(
			data.ChannelType,
		), ActionID: data.ActionID, OperatorUID: data.OperatorUID, Inputs: inputs,
	}, nil
}

func (a cardAction) String() string {
	// Logs must never contain submitted form values or untrusted payload data.
	return fmt.Sprintf("card action event %d", a.EventID)
}
