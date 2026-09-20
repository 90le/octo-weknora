package im

import (
	"context"
	"time"
)

type DeliveryEvent struct {
	MessageID string    `json:"message_id"`
	State     string    `json:"state"`
	Attempts  int       `json:"attempts"`
	ErrorCode string    `json:"error_code"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *Service) OctoDeliveryEvents(ctx context.Context, tenant uint64, channelID string) ([]DeliveryEvent, error) {
	channel, err := s.GetChannelByIDAndTenant(channelID, tenant)
	if err != nil || channel == nil || channel.Platform != "octo" {
		return nil, ErrScopeDenied
	}
	rows := []DeliveryEvent{}
	err = s.db.WithContext(ctx).Table("octo_message_inbox").Select("message_id,state,attempts,error_code,created_at,updated_at").Where("tenant_id = ? AND channel_id = ?", tenant, channelID).Order("created_at DESC").Limit(100).Scan(&rows).Error
	return rows, err
}
