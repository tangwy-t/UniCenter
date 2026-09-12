package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	jwtPkg "github.com/tangwy-t/webmanager-server/internal/pkg/jwt"
	"github.com/tangwy-t/webmanager-server/internal/pkg/logger"
	"go.uber.org/zap"
)

const eventNoticePush = "notice.push"
const eventUserKick = "user.kick"

// HubInterface is consumer-side minimal: client.go uses Unregister +
// handleAuth, handler.go uses the pump tracking. SetOnUserOnline /
// PushToUsers / PushToAll remain on the concrete Hub (wireup and hub
// internals call them directly) but are not part of this contract.
type HubInterface interface {
	Unregister(c *Client)
	handleAuth(c *Client, tokenStr string)
	// PumpStarted/PumpStopped track in-flight pump goroutines so Stop can
	// wait for them: without this, shutdown proceeds to closing shared
	// resources (logger/Redis) while pumps are still using them.
	PumpStarted()
	PumpStopped()
}

// OnUserOnlineFunc is called when a user authenticates via WebSocket.
// It returns the user's unread notices as JSON byte slices for catch-up push.
type OnUserOnlineFunc func(ctx context.Context) ([]json.RawMessage, error)

// TokenValidator checks that an access token is still in the session
// whitelist. It is implemented by *session.SessionStore; declaring it here
// (consumer side) keeps the ws package independent of the session package.
type TokenValidator interface {
	IsAccessValid(ctx context.Context, token string) (bool, error)
}

// SecretGetter returns the current JWT signing secret. Declared consumer-side
// so the hub reads the secret LIVE on every authentication instead of
// snapshotting at construction: after an admin rotates sys.jwt.secret at
// runtime, HTTP auth (which reads per-request) accepts new tokens, and WS
// auth must follow the same source — a snapshot would reject every WS
// connection until process restart.
type SecretGetter interface {
	GetString(ctx context.Context, key string, defaultVal string) string
}

// Hub maintains the set of active clients and broadcasts messages to them.
// Implements EventBus interface for publishing events to Redis.
type Hub struct {
	clients map[uint64]*Client
	mu      sync.RWMutex
	pumpWG  sync.WaitGroup

	broker       BrokerInterface
	secretProv   SecretGetter
	validator    TokenValidator
	onUserOnline OnUserOnlineFunc
	logger       logger.LoggerInterface
}

func NewHub(
	broker BrokerInterface,
	secretProv SecretGetter,
	validator TokenValidator,
	logger logger.LoggerInterface,
	fn OnUserOnlineFunc,
) *Hub {
	h := &Hub{
		clients:      make(map[uint64]*Client),
		broker:       broker,
		secretProv:   secretProv,
		validator:    validator,
		onUserOnline: fn,
		logger:       logger,
	}

	// 注册 PubSub handlers（与 scheduler.New 统一风格）
	broker.Subscribe(eventNoticePush, h.handleRedisPushEvent)
	broker.Subscribe(eventUserKick, h.handleRedisKickEvent)

	return h
}

// SetOnUserOnline sets the callback for catch-up push after Hub construction.
func (h *Hub) SetOnUserOnline(fn OnUserOnlineFunc) {
	h.onUserOnline = fn
}

// Unregister removes a client from the hub. Called from Client when the
// connection is closed.
func (h *Hub) Unregister(c *Client) {
	h.mu.Lock()
	if existing, ok := h.clients[c.UserID()]; ok && existing == c {
		delete(h.clients, c.UserID())
		h.logger.Debug("ws client unregistered", zap.Uint64("userID", c.UserID()))
	}
	h.mu.Unlock()
}

// PushToUsers pushes a message to specific online users. Internal method used by Redis callbacks.
func (h *Hub) PushToUsers(_ context.Context, userIDs []uint64, msg []byte) error {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, uid := range userIDs {
		if client, ok := h.clients[uid]; ok {
			client.Send(msg)
		}
	}
	return nil
}

// PushToAll pushes a message to all online users. Internal method used by Redis callbacks.
func (h *Hub) PushToAll(_ context.Context, msg []byte) error {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, client := range h.clients {
		client.Send(msg)
	}
	return nil
}

func (h *Hub) handleAuth(c *Client, tokenStr string) {
	// Read the secret live (same source as HTTP auth) so runtime rotation
	// of sys.jwt.secret applies to WS connections without a restart.
	secret := h.secretProv.GetString(context.Background(), jwtPkg.SecretConfigKey, jwtPkg.DefaultSecretFallback)
	claims, err := jwtPkg.ParseAccessToken(tokenStr, secret)
	if err != nil {
		h.logger.Warn("ws auth failed", zap.Error(err))
		msg, _ := MarshalAuthErr("token invalid")
		c.Send(msg)
		time.AfterFunc(100*time.Millisecond, func() { c.closeConn() })
		return
	}

	// Session whitelist check: signature validity alone would keep a logged-out
	// or revoked token's WebSocket connection alive until the JWT expires.
	if h.validator != nil {
		valid, err := h.validator.IsAccessValid(context.Background(), tokenStr)
		if err != nil {
			h.logger.Warn("ws session whitelist check failed", zap.Error(err))
		} else if !valid {
			h.logger.Warn("ws auth rejected: token revoked")
			msg, _ := MarshalAuthErr("token revoked")
			c.Send(msg)
			time.AfterFunc(100*time.Millisecond, func() { c.closeConn() })
			return
		}
	}

	userID := claims.UserID
	c.SetUserID(userID)

	// Generate a unique kick token so this client won't be kicked by its own kick event.
	kickToken := fmt.Sprintf("%d-%d", time.Now().UnixNano(), rand.Int64())
	c.SetKickToken(kickToken)

	// Register the new client first, replacing any old one.
	h.mu.Lock()
	if old, existed := h.clients[userID]; existed {
		old.Kick("您的账号在其他设备登录")
	}
	h.clients[userID] = c
	h.mu.Unlock()

	// Publish kick event to Redis for cross-instance single sign-on.
	// The kickToken ensures this instance won't kick the newly registered client.
	if err := h.PublishKick(context.Background(), userID, "您的账号在其他设备登录", kickToken); err != nil {
		h.logger.Warn("ws publish kick event failed", zap.Uint64("userID", userID), zap.Error(err))
	}

	// Send auth success. user_id is sent as a string: snowflake IDs exceed
	// JavaScript's Number.MAX_SAFE_INTEGER (2^53-1) and would lose precision
	// if serialized as a JSON number.
	authOK, _ := MarshalAuthOK(userID)
	c.Send(authOK)

	h.logger.Debug("ws client authenticated", zap.Uint64("userID", userID))

	// Catch-up push: push unread notices to the newly connected user.
	if h.onUserOnline != nil {
		notices, err := h.onUserOnline(c)
		if err != nil {
			h.logger.Warn("ws onUserOnline failed", zap.Uint64("userID", userID), zap.Error(err))
			return
		}
		for _, noticeData := range notices {
			msg, err := MarshalNewNotice(noticeData)
			if err != nil {
				h.logger.Warn("ws marshal catch-up notice failed", zap.Error(err))
				continue
			}
			c.Send(msg)
		}
		h.logger.Debug("ws catch-up push completed",
			zap.Uint64("userID", userID), zap.Int("count", len(notices)))
	}
}

func (h *Hub) handleRedisPushEvent(ctx context.Context, eventType string, payload []byte) error {
	var evt PushEvent
	if err := json.Unmarshal(payload, &evt); err != nil {
		return err
	}
	msg, err := MarshalNewNotice(evt.NoticeData)
	if err != nil {
		h.logger.Warn("ws hub marshal push event failed", zap.Error(err))
		return nil
	}
	if evt.IsAll {
		return h.PushToAll(ctx, msg)
	}
	return h.PushToUsers(ctx, evt.UserIDs, msg)
}

func (h *Hub) handleRedisKickEvent(ctx context.Context, eventType string, payload []byte) error {
	var evt KickEvent
	if err := json.Unmarshal(payload, &evt); err != nil {
		return err
	}
	h.mu.Lock()
	old, ok := h.clients[evt.UserID]
	if ok {
		// Skip if the registered client is the one that initiated this kick.
		if evt.KickToken != "" && old.KickToken() == evt.KickToken {
			h.mu.Unlock()
			return nil
		}
		delete(h.clients, evt.UserID)
	}
	h.mu.Unlock()
	if ok {
		old.Kick(evt.Reason)
		h.logger.Debug("ws hub kicked user", zap.Uint64("userID", evt.UserID), zap.String("reason", evt.Reason))
	}
	return nil
}

// EventBus interface implementation

func (h *Hub) PublishNotice(ctx context.Context, evt *PushEvent) error {
	return h.broker.Publish(ctx, eventNoticePush, evt)
}

func (h *Hub) PublishKick(ctx context.Context, userID uint64, reason string, kickToken string) error {
	evt := &KickEvent{UserID: userID, Reason: reason, KickToken: kickToken}
	return h.broker.Publish(ctx, eventUserKick, evt)
}

// Stop initiates graceful shutdown by closing the send channel of every
// registered client. Each client's WritePump detects the closed channel and
// sends a WebSocket CloseMessage before closing the TCP connection. The
// ReadPump exits and triggers Unregister.
func (h *Hub) Stop() {
	h.mu.Lock()
	count := len(h.clients)
	for _, c := range h.clients {
		c.Close()
	}
	h.mu.Unlock()

	// Wait for in-flight pump goroutines (bounded so a misbehaving client
	// cannot stall shutdown). c.Close() makes ReadPump/WritePump exit;
	// each pump defers PumpStopped.
	done := make(chan struct{})
	go func() {
		h.pumpWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		h.logger.Warn("ws hub: timed out waiting for pump goroutines")
	}
	h.logger.Info("ws hub: shutdown complete", zap.Int("clients", count))
}

// PumpStarted records a pump goroutine starting (call before launching
// ReadPump/WritePump).
func (h *Hub) PumpStarted() { h.pumpWG.Add(1) }

// PumpStopped records a pump goroutine exiting.
func (h *Hub) PumpStopped() { h.pumpWG.Done() }
