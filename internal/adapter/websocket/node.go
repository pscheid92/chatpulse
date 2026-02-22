package websocket

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/centrifugal/centrifuge"
	"github.com/pscheid92/chatpulse/internal/adapter/metrics"
	"github.com/pscheid92/chatpulse/internal/domain"
	"github.com/pscheid92/uuid"
)

func NewNode(userRepo domain.StreamerRepository, wsMetrics *metrics.WebSocketMetrics, logLevel string) (*centrifuge.Node, error) {
	conf := centrifuge.Config{LogLevel: parseCentrifugeLogLevel(logLevel), LogHandler: slogHandler}
	node, err := centrifuge.New(conf)
	if err != nil {
		return nil, fmt.Errorf("create centrifuge node: %w", err)
	}

	node.OnConnecting(onConnecting(userRepo))
	node.OnConnect(onConnect(wsMetrics))

	return node, nil
}

func onConnecting(userRepo domain.StreamerRepository) func(ctx context.Context, e centrifuge.ConnectEvent) (centrifuge.ConnectReply, error) {
	return func(ctx context.Context, e centrifuge.ConnectEvent) (centrifuge.ConnectReply, error) {
		cred, ok := centrifuge.GetCredentials(ctx)
		if !ok || cred.UserID == "" {
			return centrifuge.ConnectReply{}, centrifuge.DisconnectServerError
		}

		overlayUUID, err := uuid.Parse(cred.UserID)
		if err != nil {
			slog.Warn("Invalid overlay UUID", "overlay_uuid", cred.UserID, "error", err)
			return centrifuge.ConnectReply{}, centrifuge.DisconnectServerError
		}

		user, err := userRepo.GetByOverlayUUID(ctx, overlayUUID)
		if err != nil {
			slog.Warn("Failed to resolve overlay UUID", "overlay_uuid", cred.UserID, "error", err)
			return centrifuge.ConnectReply{}, centrifuge.DisconnectServerError
		}

		channel := "sentiment:" + user.TwitchUserID

		reply := centrifuge.ConnectReply{
			Subscriptions: map[string]centrifuge.SubscribeOptions{
				channel: {
					EmitPresence: true,
				},
			},
		}
		return reply, nil
	}
}

func onConnect(wsMetrics *metrics.WebSocketMetrics) func(client *centrifuge.Client) {
	return func(client *centrifuge.Client) {
		slog.Debug("Client connected", "client_id", client.ID(), "user_id", client.UserID())

		if wsMetrics != nil {
			wsMetrics.ActiveConnections.Inc()
		}

		client.OnSubscribe(func(e centrifuge.SubscribeEvent, cb centrifuge.SubscribeCallback) {
			options := centrifuge.SubscribeOptions{EmitPresence: true}
			cb(centrifuge.SubscribeReply{Options: options}, nil)
		})

		client.OnDisconnect(func(e centrifuge.DisconnectEvent) {
			slog.Debug("Client disconnected", "client_id", client.ID(), "reason", e.Reason)
			if wsMetrics != nil {
				wsMetrics.ActiveConnections.Dec()
			}
		})
	}
}

func SetupRedis(node *centrifuge.Node, redisAddr string) error {
	shardConfig := centrifuge.RedisShardConfig{Address: redisAddr}
	shard, err := centrifuge.NewRedisShard(node, shardConfig)
	if err != nil {
		return fmt.Errorf("create redis shard: %w", err)
	}

	brokerConfig := centrifuge.RedisBrokerConfig{Prefix: "chatpulse", Shards: []*centrifuge.RedisShard{shard}}
	broker, err := centrifuge.NewRedisBroker(node, brokerConfig)
	if err != nil {
		return fmt.Errorf("create redis broker: %w", err)
	}
	node.SetBroker(broker)

	pmConfig := centrifuge.RedisPresenceManagerConfig{Prefix: "chatpulse", Shards: []*centrifuge.RedisShard{shard}}
	presenceManager, err := centrifuge.NewRedisPresenceManager(node, pmConfig)
	if err != nil {
		return fmt.Errorf("create redis presence manager: %w", err)
	}
	node.SetPresenceManager(presenceManager)

	return nil
}

func slogHandler(entry centrifuge.LogEntry) {
	attrs := make([]any, 0, len(entry.Fields)*2)
	for k, v := range entry.Fields {
		attrs = append(attrs, k, v)
	}
	switch entry.Level {
	case centrifuge.LogLevelDebug:
		slog.Debug(entry.Message, attrs...)
	case centrifuge.LogLevelInfo:
		slog.Info(entry.Message, attrs...)
	case centrifuge.LogLevelWarn:
		slog.Warn(entry.Message, attrs...)
	case centrifuge.LogLevelError:
		slog.Error(entry.Message, attrs...)
	case centrifuge.LogLevelTrace:
		slog.Debug(entry.Message, attrs...)
	case centrifuge.LogLevelNone:
		// EMPTY
	}
}

func parseCentrifugeLogLevel(level string) centrifuge.LogLevel {
	switch level {
	case "debug":
		return centrifuge.LogLevelDebug
	case "warn":
		return centrifuge.LogLevelWarn
	case "error":
		return centrifuge.LogLevelError
	default:
		return centrifuge.LogLevelInfo
	}
}

type PresenceChecker struct {
	node *centrifuge.Node
}

func NewPresenceChecker(node *centrifuge.Node) *PresenceChecker {
	return &PresenceChecker{node: node}
}

func (p *PresenceChecker) HasViewers(broadcasterID string) bool {
	stats, err := p.node.PresenceStats("sentiment:" + broadcasterID)
	return err == nil && stats.NumClients > 0
}

func (p *PresenceChecker) ViewerCount(broadcasterID string) int {
	stats, err := p.node.PresenceStats("sentiment:" + broadcasterID)
	if err != nil {
		return 0
	}
	return stats.NumClients
}
