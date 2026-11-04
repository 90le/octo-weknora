package types

import "time"

// ChannelConversationMessage exposes only the user-visible local transcript.
type ChannelConversationMessage struct {
	ID        string    `json:"id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}
