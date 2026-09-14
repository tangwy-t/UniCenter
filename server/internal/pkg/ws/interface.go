package ws

import (
	"context"
	"github.com/tangwy-t/WebManagerFramework/server/internal/pkg/redis/pubsub"
)

type BrokerInterface interface {
	Subscribe(eventType string, handler pubsub.Handler)
	Publish(ctx context.Context, eventType string, payload any) error
}
