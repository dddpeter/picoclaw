// PicoClaw - Ultra-lightweight personal AI agent
// Inspired by and based on nanobot: https://github.com/HKUDS/nanobot
// License: MIT
//
// Copyright (c) 2026 PicoClaw contributors

package channels

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"runtime/debug"
	"sort"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/sipeed/picoclaw/pkg/bus"
	"github.com/sipeed/picoclaw/pkg/config"
	runtimeevents "github.com/sipeed/picoclaw/pkg/events"
	"github.com/sipeed/picoclaw/pkg/logger"
	"github.com/sipeed/picoclaw/pkg/media"
)

const (
	defaultChannelQueueSize = 64
	defaultRateLimit        = 10 // default 10 msg/s
	maxRetries              = 3
	rateLimitDelay          = 1 * time.Second
	baseBackoff             = 500 * time.Millisecond
	maxBackoff              = 8 * time.Second

	janitorInterval = 10 * time.Second
	typingStopTTL   = 5 * time.Minute
	placeholderTTL  = 10 * time.Minute

	streamAuxiliaryTombstoneTTL = 30 * time.Second
)

type channelWorker struct {
	ch         Channel
	queue      chan bus.OutboundMessage
	mediaQueue chan bus.OutboundMediaMessage
	done       chan struct{}
	mediaDone  chan struct{}
	limiter    *rate.Limiter
	// cancel stops this worker's goroutines. Queues are intentionally never
	// closed: dispatcher goroutines may still hold a reference and a send on
	// a closed channel panics (race-detector-visible under load).
	cancel context.CancelFunc
}

type Manager struct {
	channels                  map[string]Channel
	workers                   map[string]*channelWorker
	bus                       *bus.MessageBus
	runtimeEvents             runtimeevents.Bus
	config                    *config.Config
	mediaStore                media.MediaStore
	dispatchTask              *asyncTask
	mux                       *dynamicServeMux
	httpServer                *http.Server
	httpListeners             []net.Listener
	mu                        sync.RWMutex
	placeholders              sync.Map          // "channel:chatID" → placeholderID (string)
	typingStops               sync.Map          // "channel:chatID" → func()
	reactionUndos             sync.Map          // "channel:chatID" → reactionEntry
	streamActive              sync.Map          // streamSuppressionKey → true (set when streamer.Finalize sent the message)
	streamAuxiliaryTombstones sync.Map          // streamSuppressionKey → time.Time (drops late auxiliary messages after stream final)
	channelHashes             map[string]string // channel name → config hash
}

type mediaStoreSetter interface {
	SetMediaStore(s media.MediaStore)
}

// ManagerOption configures a channel Manager.
type ManagerOption func(*Manager)

// WithRuntimeEvents injects the runtime event bus used for channel observations.
func WithRuntimeEvents(eventBus runtimeevents.Bus) ManagerOption {
	return func(m *Manager) {
		m.runtimeEvents = eventBus
	}
}

// ChannelLifecyclePayload describes channel lifecycle runtime events.
type ChannelLifecyclePayload struct {
	Type  string `json:"type,omitempty"`
	Error string `json:"error,omitempty"`
}

// ChannelOutboundPayload describes channel outbound message runtime events.
type ChannelOutboundPayload struct {
	Media            bool     `json:"media,omitempty"`
	ContentLen       int      `json:"content_len,omitempty"`
	MessageIDs       []string `json:"message_ids,omitempty"`
	ReplyToMessageID string   `json:"reply_to_message_id,omitempty"`
	Error            string   `json:"error,omitempty"`
	Retries          int      `json:"retries,omitempty"`
}

func NewManager(
	cfg *config.Config,
	messageBus *bus.MessageBus,
	store media.MediaStore,
	opts ...ManagerOption,
) (*Manager, error) {
	m := &Manager{
		channels:      make(map[string]Channel),
		workers:       make(map[string]*channelWorker),
		bus:           messageBus,
		config:        cfg,
		mediaStore:    store,
		channelHashes: make(map[string]string),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(m)
		}
	}

	// Register as streaming delegate so the agent loop can obtain streamers
	messageBus.SetStreamDelegate(m)

	if err := m.initChannels(&cfg.Channels); err != nil {
		return nil, err
	}

	// Store initial config hashes for all channels
	m.channelHashes = toChannelHashes(cfg)

	return m, nil
}

// SetMediaStore updates the store used by the manager and every channel that
// accepts media store injection. Gateway reload creates a fresh store, so
// keeping existing channels on the same store as the agent is required for
// inbound media refs to remain resolvable after reload.
func (m *Manager) SetMediaStore(store media.MediaStore) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.mediaStore = store
	for _, ch := range m.channels {
		if setter, ok := ch.(mediaStoreSetter); ok {
			setter.SetMediaStore(store)
		}
	}
}

// NotifySteering implements bus.SteeringNotifyDelegate: forwards a steering
// acknowledgement to the channel's active streaming surface, if any.
func (m *Manager) NotifySteering(ctx context.Context, channelName, chatID, sessionKey, preview string) bool {
	m.mu.RLock()
	ch, exists := m.channels[channelName]
	m.mu.RUnlock()
	if !exists {
		return false
	}
	notifier, ok := ch.(SteeringNotifyCapable)
	if !ok {
		return false
	}
	return notifier.NotifySteeringInChat(ctx, chatID, sessionKey, preview)
}

// initChannel is a helper that looks up a factory by type name and creates the channel.
// typeName is the channel type used for factory lookup (e.g., "telegram").
// channelName is the config map key used as the channel's runtime name (e.g., "my_telegram").
func (m *Manager) initChannel(typeName, channelName string) {
	f, ok := getFactory(typeName)
	if !ok {
		logger.WarnCF("channels", "Factory not registered", map[string]any{
			"channel": channelName,
			"type":    typeName,
		})
		return
	}
	logger.DebugCF("channels", "Attempting to initialize channel", map[string]any{
		"channel": channelName,
		"type":    typeName,
	})
	ch, err := f(channelName, typeName, m.config, m.bus)
	if err != nil {
		logger.ErrorCF("channels", "Failed to initialize channel", map[string]any{
			"channel": channelName,
			"type":    typeName,
			"error":   err.Error(),
		})
	} else {
		// Inject MediaStore if channel supports it
		if m.mediaStore != nil {
			if setter, ok := ch.(mediaStoreSetter); ok {
				setter.SetMediaStore(m.mediaStore)
			}
		}
		// Inject PlaceholderRecorder if channel supports it
		if setter, ok := ch.(interface{ SetPlaceholderRecorder(r PlaceholderRecorder) }); ok {
			setter.SetPlaceholderRecorder(m)
		}
		// Inject owner reference so BaseChannel.HandleMessage can auto-trigger typing/reaction
		if setter, ok := ch.(interface{ SetOwner(ch Channel) }); ok {
			setter.SetOwner(ch)
		}
		m.channels[channelName] = ch
		m.publishChannelEvent(
			runtimeevents.KindChannelLifecycleInitialized,
			channelName,
			runtimeevents.Scope{Channel: channelName},
			runtimeevents.SeverityInfo,
			ChannelLifecyclePayload{Type: typeName},
		)
		logger.InfoCF("channels", "Channel enabled successfully", map[string]any{
			"channel": channelName,
			"type":    typeName,
		})
	}
}

func (m *Manager) getChannelConfigAndEnabled(channelName string) (*config.Channel, bool) {
	bc, ok := m.config.Channels[channelName]
	if !ok || bc == nil {
		return nil, false
	}
	if !bc.Enabled {
		return bc, false
	}

	// Use Type to determine the config struct for validation.
	// The map key (channelName) is the config key, which may differ from the type.
	channelType := bc.Type
	if channelType == "" {
		channelType = channelName
	}

	// Settings have already been decoded by InitChannelList, so we just need to
	// type-assert and check the relevant fields.
	decoded, err := bc.GetDecoded()
	if err != nil {
		return bc, false
	}
	//nolint:revive
	switch settings := decoded.(type) {
	case *config.WhatsAppSettings:
		if channelType == config.ChannelWhatsApp {
			return bc, settings.BridgeURL != ""
		}
		return bc, channelType == config.ChannelWhatsAppNative && settings.UseNative
	case *config.MatrixSettings:
		return bc, settings.Homeserver != "" && settings.UserID != "" && settings.AccessToken.String() != ""
	case *config.WeComSettings:
		return bc, settings.BotID != "" && settings.Secret.String() != ""
	case *config.PicoClientSettings:
		return bc, settings.URL != ""
	case *config.DingTalkSettings:
		return bc, settings.ClientID != ""
	case *config.SlackSettings:
		return bc, settings.BotToken.String() != ""
	case *config.WeixinSettings:
		return bc, settings.Token.String() != ""
	case *config.PicoSettings:
		return bc, settings.Token.String() != ""
	case *config.IRCSettings:
		return bc, settings.Server != ""
	case *config.LINESettings:
		return bc, settings.ChannelAccessToken.String() != ""
	case *config.OneBotSettings:
		return bc, settings.WSUrl != ""
	case *config.QQSettings:
		return bc, settings.AppSecret.String() != ""
	case *config.TelegramSettings:
		return bc, settings.Token.String() != ""
	case *config.FeishuSettings:
		return bc, settings.AppSecret.String() != ""
	case *config.MaixCamSettings:
		return bc, true
	case *config.TeamsWebhookSettings:
		return bc, true
	case *config.SlackWebhookSettings:
		return bc, true
	case *config.DiscordSettings:
		return bc, settings.Token.String() != ""
	case *config.VKSettings:
		return bc, settings.GroupID != 0 && settings.Token.String() != ""
	case *config.MQTTSettings:
		return bc, settings.Broker != "" && settings.AgentID != ""
	}

	return bc, bc.Enabled
}

// initChannels initializes all enabled channels based on the configuration.
// It iterates config entries and uses bc.Type to look up the appropriate factory.
func (m *Manager) initChannels(channels *config.ChannelsConfig) error {
	logger.InfoC("channels", "Initializing channel manager")

	for name, bc := range *channels {
		if !bc.Enabled {
			continue
		}
		_, ready := m.getChannelConfigAndEnabled(name)
		if !ready {
			continue
		}
		typeName := bc.Type
		if typeName == "" {
			typeName = name
		}
		m.initChannel(typeName, name)
	}

	logger.InfoCF("channels", "Channel initialization completed", map[string]any{
		"enabled_channels": len(m.channels),
	})

	return nil
}

func (m *Manager) StartAll(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.channels) == 0 {
		logger.WarnC("channels", "No channels enabled")
	}

	logger.InfoC("channels", "Starting all channels")

	dispatchCtx, cancel := context.WithCancel(ctx)
	m.dispatchTask = &asyncTask{cancel: cancel}
	failedStarts := make([]error, 0, len(m.channels))
	failedNames := make([]string, 0, len(m.channels))

	for name, channel := range m.channels {
		logger.InfoCF("channels", "Starting channel", map[string]any{
			"channel": name,
		})
		if err := channel.Start(ctx); err != nil {
			logger.ErrorCF("channels", "Failed to start channel", map[string]any{
				"channel": name,
				"error":   err.Error(),
			})
			m.publishChannelEvent(
				runtimeevents.KindChannelLifecycleStartFailed,
				name,
				runtimeevents.Scope{Channel: name},
				runtimeevents.SeverityError,
				ChannelLifecyclePayload{Type: channelTypeForEvent(m, name), Error: err.Error()},
			)
			failedStarts = append(failedStarts, fmt.Errorf("channel %s: %w", name, err))
			failedNames = append(failedNames, name)
			continue
		}
		// Lazily create worker only after channel starts successfully
		channelType := name
		if m.config != nil {
			if bc := m.config.Channels.Get(name); bc != nil && bc.Type != "" {
				channelType = bc.Type
			}
		}
		w := newChannelWorker(name, channel, channelType)
		workerCtx, workerCancel := context.WithCancel(dispatchCtx)
		w.cancel = workerCancel
		m.workers[name] = w
		go m.runWorker(workerCtx, name, w)
		go m.runMediaWorker(workerCtx, name, w)
		m.publishChannelEvent(
			runtimeevents.KindChannelLifecycleStarted,
			name,
			runtimeevents.Scope{Channel: name},
			runtimeevents.SeverityInfo,
			ChannelLifecyclePayload{Type: channelType},
		)
	}

	if len(m.channels) > 0 && len(m.workers) == 0 {
		if m.dispatchTask != nil {
			m.dispatchTask.cancel()
			m.dispatchTask = nil
		}

		sort.Strings(failedNames)
		if len(failedStarts) == 0 {
			return fmt.Errorf("failed to start any enabled channels")
		}

		logger.ErrorCF("channels", "All enabled channels failed to start", map[string]any{
			"failed":          len(failedNames),
			"total":           len(m.channels),
			"failed_channels": failedNames,
		})

		return fmt.Errorf("failed to start any enabled channels: %w", errors.Join(failedStarts...))
	}

	if len(failedNames) > 0 {
		sort.Strings(failedNames)
		logger.WarnCF("channels", "Some channels failed to start", map[string]any{
			"failed":          len(failedNames),
			"started":         len(m.workers),
			"total":           len(m.channels),
			"failed_channels": failedNames,
		})
	}

	// Start the dispatcher that reads from the bus and routes to workers
	go m.dispatchOutbound(dispatchCtx)
	go m.dispatchOutboundMedia(dispatchCtx)

	// Start the TTL janitor that cleans up stale typing/placeholder entries
	go m.runTTLJanitor(dispatchCtx)

	// Start shared HTTP server if configured
	if m.httpServer != nil {
		if len(m.httpListeners) > 0 {
			for _, listener := range m.httpListeners {
				ln := listener
				go func() {
					defer func() {
						if r := recover(); r != nil {
							logger.ErrorCF("channels", "HTTP server goroutine panic recovered",
								map[string]any{
									"addr":  ln.Addr().String(),
									"panic": fmt.Sprintf("%v", r),
									"stack": string(debug.Stack()),
								})
						}
					}()
					logger.InfoCF("channels", "Shared HTTP server listening", map[string]any{
						"addr": ln.Addr().String(),
					})
					if err := m.httpServer.Serve(ln); err != nil && err != http.ErrServerClosed {
						logger.FatalCF("channels", "Shared HTTP server error", map[string]any{
							"addr":  ln.Addr().String(),
							"error": err.Error(),
						})
					}
				}()
			}
		} else {
			go func() {
				defer func() {
					if r := recover(); r != nil {
						logger.ErrorCF("channels", "HTTP server goroutine panic recovered",
							map[string]any{
								"addr":  m.httpServer.Addr,
								"panic": fmt.Sprintf("%v", r),
								"stack": string(debug.Stack()),
							})
					}
				}()
				logger.InfoCF("channels", "Shared HTTP server listening", map[string]any{
					"addr": m.httpServer.Addr,
				})
				if err := m.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
					logger.FatalCF("channels", "Shared HTTP server error", map[string]any{
						"error": err.Error(),
					})
				}
			}()
		}
	}

	logger.InfoCF("channels", "Channel startup completed", map[string]any{
		"started": len(m.workers),
		"failed":  len(failedNames),
		"total":   len(m.channels),
	})
	return nil
}

func (m *Manager) StopAll(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	logger.InfoC("channels", "Stopping all channels")

	// Shutdown shared HTTP server first
	if m.httpServer != nil {
		shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := m.httpServer.Shutdown(shutdownCtx); err != nil {
			logger.ErrorCF("channels", "Shared HTTP server shutdown error", map[string]any{
				"error": err.Error(),
			})
		}
		m.httpServer = nil
		m.httpListeners = nil
	}

	// Cancel dispatcher
	if m.dispatchTask != nil {
		m.dispatchTask.cancel()
		m.dispatchTask = nil
	}

	// The dispatch-context cancel above stops every worker goroutine (each
	// runs on a child context). Queues are never closed: a dispatcher may
	// still be mid-send on one, and sending on a closed channel panics.
	for _, w := range m.workers {
		if w != nil {
			<-w.done
		}
	}
	for _, w := range m.workers {
		if w != nil {
			<-w.mediaDone
		}
	}

	// Stop all channels
	for name, channel := range m.channels {
		logger.InfoCF("channels", "Stopping channel", map[string]any{
			"channel": name,
		})
		if err := channel.Stop(ctx); err != nil {
			logger.ErrorCF("channels", "Error stopping channel", map[string]any{
				"channel": name,
				"error":   err.Error(),
			})
			continue
		}
		m.publishChannelEvent(
			runtimeevents.KindChannelLifecycleStopped,
			name,
			runtimeevents.Scope{Channel: name},
			runtimeevents.SeverityInfo,
			ChannelLifecyclePayload{Type: channelTypeForEvent(m, name)},
		)
	}

	logger.InfoC("channels", "All channels stopped")
	return nil
}

func (m *Manager) GetChannel(name string) (Channel, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	channel, ok := m.channels[name]
	return channel, ok
}

func (m *Manager) GetStatus() map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()

	status := make(map[string]any)
	for name, channel := range m.channels {
		status[name] = map[string]any{
			"enabled": true,
			"running": channel.IsRunning(),
		}
	}
	return status
}

func (m *Manager) GetEnabledChannels() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, 0, len(m.channels))
	for name := range m.channels {
		names = append(names, name)
	}
	return names
}

// Reload updates the config reference without restarting channels.
// This is used when channel config hasn't changed but other parts of the config have.
func (m *Manager) Reload(ctx context.Context, cfg *config.Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Save old config so we can revert on error.
	oldConfig := m.config

	// Update config early: initChannel uses m.config via factory(m.config, m.bus).
	m.config = cfg

	list := toChannelHashes(cfg)
	added, removed := compareChannels(m.channelHashes, list)

	deferFuncs := make([]func(), 0, len(removed)+len(added))
	for _, name := range removed {
		// Stop all channels
		channel := m.channels[name]
		logger.InfoCF("channels", "Stopping channel", map[string]any{
			"channel": name,
		})
		if err := channel.Stop(ctx); err != nil {
			logger.ErrorCF("channels", "Error stopping channel", map[string]any{
				"channel": name,
				"error":   err.Error(),
			})
		}
		deferFuncs = append(deferFuncs, func() {
			m.UnregisterChannel(name)
		})
	}
	dispatchCtx, cancel := context.WithCancel(ctx)
	m.dispatchTask = &asyncTask{cancel: cancel}
	cc, err := toChannelConfig(cfg, added)
	if err != nil {
		logger.ErrorC("channels", fmt.Sprintf("toChannelConfig error: %v", err))
		m.config = oldConfig
		cancel()
		return err
	}
	err = m.initChannels(cc)
	if err != nil {
		logger.ErrorC("channels", fmt.Sprintf("initChannels error: %v", err))
		m.config = oldConfig
		cancel()
		return err
	}
	for _, name := range added {
		channel := m.channels[name]
		logger.InfoCF("channels", "Starting channel", map[string]any{
			"channel": name,
		})
		if err := channel.Start(ctx); err != nil {
			logger.ErrorCF("channels", "Failed to start channel", map[string]any{
				"channel": name,
				"error":   err.Error(),
			})
			m.publishChannelEvent(
				runtimeevents.KindChannelLifecycleStartFailed,
				name,
				runtimeevents.Scope{Channel: name},
				runtimeevents.SeverityError,
				ChannelLifecyclePayload{Type: channelTypeForEvent(m, name), Error: err.Error()},
			)
			continue
		}
		// Lazily create worker only after channel starts successfully
		channelType := name
		if m.config != nil {
			if bc := m.config.Channels.Get(name); bc != nil && bc.Type != "" {
				channelType = bc.Type
			}
		}
		w := newChannelWorker(name, channel, channelType)
		workerCtx, workerCancel := context.WithCancel(dispatchCtx)
		w.cancel = workerCancel
		m.workers[name] = w
		go m.runWorker(workerCtx, name, w)
		go m.runMediaWorker(workerCtx, name, w)
		m.publishChannelEvent(
			runtimeevents.KindChannelLifecycleStarted,
			name,
			runtimeevents.Scope{Channel: name},
			runtimeevents.SeverityInfo,
			ChannelLifecyclePayload{Type: channelType},
		)
		deferFuncs = append(deferFuncs, func() {
			m.RegisterChannel(name, channel)
		})
	}

	// Commit hashes only on full success.
	m.channelHashes = list
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.ErrorCF("channels", "channel registration goroutine panic recovered",
					map[string]any{
						"panic": fmt.Sprintf("%v", r),
						"stack": string(debug.Stack()),
					})
			}
		}()
		for _, f := range deferFuncs {
			f()
		}
	}()
	return nil
}

func (m *Manager) RegisterChannel(name string, channel Channel) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.channels[name] = channel
	if m.mux != nil {
		m.registerChannelHTTPHandler(name, channel)
	}
}

func (m *Manager) UnregisterChannel(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ch, ok := m.channels[name]; ok && m.mux != nil {
		m.unregisterChannelHTTPHandler(name, ch)
	}
	if w, ok := m.workers[name]; ok && w != nil {
		if w.cancel != nil {
			w.cancel()
		}
		<-w.done
		<-w.mediaDone
	}
	delete(m.workers, name)
	delete(m.channels, name)
}

// SendMessage sends an outbound message synchronously through the channel
// worker's rate limiter and retry logic. It blocks until the message is
// delivered (or all retries are exhausted), which preserves ordering when
// a subsequent operation depends on the message having been sent.
func (m *Manager) SendMessage(ctx context.Context, msg bus.OutboundMessage) error {
	msg = bus.NormalizeOutboundMessage(msg)
	channelName := outboundMessageChannel(msg)

	m.mu.RLock()
	_, exists := m.channels[channelName]
	w, wExists := m.workers[channelName]
	m.mu.RUnlock()

	if !exists {
		return fmt.Errorf("channel %s not found", channelName)
	}
	if !wExists || w == nil {
		return fmt.Errorf("channel %s has no active worker", channelName)
	}

	maxLen := 0
	if mlp, ok := w.ch.(MessageLengthProvider); ok {
		maxLen = mlp.MaxMessageLength()
	}
	if chunks := splitOutboundMessageContent(msg, maxLen); len(chunks) > 1 {
		for _, chunk := range chunks {
			chunkMsg := msg
			chunkMsg.Content = chunk
			m.sendWithRetry(ctx, channelName, w, chunkMsg)
		}
	} else {
		if len(chunks) == 1 {
			msg.Content = chunks[0]
		}
		m.sendWithRetry(ctx, channelName, w, msg)
	}
	return nil
}

// SendMedia sends outbound media synchronously through the channel worker's
// rate limiter and retry logic. It blocks until the media is delivered (or all
// retries are exhausted), which preserves ordering when later agent behavior
// depends on actual media delivery.
func (m *Manager) SendMedia(ctx context.Context, msg bus.OutboundMediaMessage) error {
	msg = bus.NormalizeOutboundMediaMessage(msg)
	channelName := outboundMediaChannel(msg)

	m.mu.RLock()
	_, exists := m.channels[channelName]
	w, wExists := m.workers[channelName]
	m.mu.RUnlock()

	if !exists {
		return fmt.Errorf("channel %s not found", channelName)
	}
	if !wExists || w == nil {
		return fmt.Errorf("channel %s has no active worker", channelName)
	}

	_, err := m.sendMediaWithRetry(ctx, channelName, w, msg)
	return err
}

func (m *Manager) SendToChannel(ctx context.Context, channelName, chatID, content string) error {
	m.mu.RLock()
	_, exists := m.channels[channelName]
	w, wExists := m.workers[channelName]
	m.mu.RUnlock()

	if !exists {
		return fmt.Errorf("channel %s not found", channelName)
	}

	msg := bus.OutboundMessage{
		Context: bus.NewOutboundContext(channelName, chatID, ""),
		Content: content,
	}
	msg = bus.NormalizeOutboundMessage(msg)

	if wExists && w != nil {
		select {
		case w.queue <- msg:
			m.publishOutboundQueued(channelName, msg)
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// Fallback: direct send (should not happen)
	channel, _ := m.channels[channelName]
	_, err := channel.Send(ctx, msg)
	return err
}

// Compile-time assurance that the manager's streamer wrappers preserve the
// optional capability interfaces the agent pushes tool steps through.
var (
	_ bus.ToolStepStreamer     = (*finalizeHookStreamer)(nil)
	_ bus.ToolStepStreamer     = (*splitMarkerStreamer)(nil)
	_ bus.CancelReasonStreamer = (*finalizeHookStreamer)(nil)
	_ bus.CancelReasonStreamer = (*splitMarkerStreamer)(nil)
)
