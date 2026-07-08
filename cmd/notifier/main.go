// Command stratos-notifier is the OpenStack→Stratos notification bridge.
//
// Stratos ships only the notification RECEIVER (the /api/v1/notifications/{id}/{region}
// webhook); it never dials a cloud's message broker. This bridge is the missing half: it
// consumes oslo.messaging notifications from the cloud's RabbitMQ and re-posts each one to the
// Stratos webhook with the shared-secret header, giving near-real-time cache updates.
//
// Run one instance per (cloud, region) — the webhook URL is region-scoped. See
// docs/openstack-notifications.md.
//
// Config (environment):
//
//	RABBITMQ_URL        full amqp URI (wins if set), e.g. amqp://user:pass@host:5672/vhost
//	RABBITMQ_ADDRESSES  else: comma list host:port tried in order (with USERNAME/PASSWORD)
//	RABBITMQ_USERNAME   default "openstack"
//	RABBITMQ_PASSWORD   (required unless RABBITMQ_URL carries it)
//	RABBITMQ_EXCHANGES  comma list, default "nova,neutron,cinder,glance,heat,magnum,manila,designate"
//	RABBITMQ_QUEUE      durable queue name, default "stratos-notifier"
//	RABBITMQ_TOPIC      binding key, default "notifications.#"
//	RABBITMQ_PREFETCH   in-flight messages, default 20
//	TARGET_URL          the Stratos Notifier URI (required)
//	TARGET_SECRET       the provider's notification secret (required)
//	PORT                health server port, default 7476
package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const notificationSecretHeader = "X-Stratos-Notification-Secret"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := loadConfig()
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(2)
	}

	conn, err := dial(cfg)
	if err != nil {
		log.Error("rabbitmq connect", "err", err)
		os.Exit(1)
	}
	defer func() { _ = conn.Close() }()
	log.Info("connected to rabbitmq", "queue", cfg.queue, "exchanges", cfg.exchanges, "target", cfg.targetURL)

	// A broker-side connection drop can't be silently ridden out — exit non-zero and let the
	// orchestrator restart the pod (simpler and safer than hand-rolled reconnect).
	closed := conn.NotifyClose(make(chan *amqp.Error, 1))
	go func() {
		if e := <-closed; e != nil {
			log.Error("rabbitmq connection closed", "err", e)
			os.Exit(1)
		}
	}()

	ch, err := conn.Channel()
	if err != nil {
		log.Error("open channel", "err", err)
		os.Exit(1)
	}
	if _, err := ch.QueueDeclare(cfg.queue, true, false, false, false, nil); err != nil {
		log.Error("declare queue", "err", err)
		os.Exit(1)
	}
	bindExchanges(conn, cfg, log)
	if err := ch.Qos(cfg.prefetch, 0, false); err != nil {
		log.Error("qos", "err", err)
		os.Exit(1)
	}
	deliveries, err := ch.Consume(cfg.queue, "", false, false, false, false, nil)
	if err != nil {
		log.Error("consume", "err", err)
		os.Exit(1)
	}

	go serveHealth(cfg.port, conn, log)

	client := &http.Client{Timeout: 15 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Info("bridge running", "port", cfg.port)
	for {
		select {
		case <-ctx.Done():
			log.Info("shutting down")
			_ = ch.Close()
			return
		case d, ok := <-deliveries:
			if !ok {
				log.Error("delivery channel closed")
				os.Exit(1)
			}
			forward(ctx, client, cfg, log, d)
		}
	}
}

// forward posts one notification body to the Stratos webhook. 2xx → ack. A 4xx is a
// non-retryable client error (bad secret / malformed) → log and ack (drop) so a poison message
// can't loop forever; the periodic sync is the safety net. Anything else (5xx, network) → nack
// with requeue, throttled so a down target doesn't hot-loop.
func forward(ctx context.Context, client *http.Client, cfg config, log *slog.Logger, d amqp.Delivery) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.targetURL, bytes.NewReader(d.Body))
	if err != nil {
		log.Error("build request", "err", err)
		_ = d.Nack(false, true)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "stratos-notifier")
	req.Header.Set(notificationSecretHeader, cfg.targetSecret)

	resp, err := client.Do(req)
	if err != nil {
		log.Warn("post failed, requeuing", "err", err)
		time.Sleep(2 * time.Second)
		_ = d.Nack(false, true)
		return
	}
	_ = resp.Body.Close()
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		_ = d.Ack(false)
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		// 401 = wrong/absent secret, 400 = malformed. Retrying can't fix either; drop + log loudly.
		log.Error("webhook rejected (non-retryable), dropping event", "status", resp.StatusCode)
		_ = d.Ack(false)
	default:
		log.Warn("webhook 5xx, requeuing", "status", resp.StatusCode)
		time.Sleep(2 * time.Second)
		_ = d.Nack(false, true)
	}
}

// bindExchanges binds the queue to each OpenStack notification exchange on its own channel, so a
// missing service (its exchange absent) is logged and skipped rather than killing the bridge.
func bindExchanges(conn *amqp.Connection, cfg config, log *slog.Logger) {
	bound := 0
	for _, ex := range cfg.exchanges {
		ch, err := conn.Channel()
		if err != nil {
			log.Error("bind: open channel", "exchange", ex, "err", err)
			continue
		}
		// oslo notification exchanges are topic + durable; an idempotent declare tolerates an
		// already-existing matching exchange and creates one that a not-yet-started service will use.
		if err := ch.ExchangeDeclare(ex, "topic", true, false, false, false, nil); err != nil {
			log.Warn("bind: declare exchange failed, skipping", "exchange", ex, "err", err)
			_ = ch.Close()
			continue
		}
		if err := ch.QueueBind(cfg.queue, cfg.bindingKey, ex, false, nil); err != nil {
			log.Warn("bind: queue bind failed, skipping", "exchange", ex, "err", err)
			_ = ch.Close()
			continue
		}
		_ = ch.Close()
		bound++
	}
	log.Info("bound exchanges", "count", bound, "of", len(cfg.exchanges))
}

func serveHealth(port int, conn *amqp.Connection, log *slog.Logger) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		if conn.IsClosed() {
			http.Error(w, "rabbitmq closed", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	srv := &http.Server{Addr: fmt.Sprintf(":%d", port), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Error("health server", "err", err)
	}
}

type config struct {
	url          string
	addresses    []string
	username     string
	password     string
	exchanges    []string
	queue        string
	bindingKey   string
	prefetch     int
	targetURL    string
	targetSecret string
	port         int
}

func loadConfig() (config, error) {
	c := config{
		url:          os.Getenv("RABBITMQ_URL"),
		username:     envOr("RABBITMQ_USERNAME", "openstack"),
		password:     os.Getenv("RABBITMQ_PASSWORD"),
		queue:        envOr("RABBITMQ_QUEUE", "stratos-notifier"),
		bindingKey:   envOr("RABBITMQ_TOPIC", "notifications.#"),
		exchanges:    splitCSV(envOr("RABBITMQ_EXCHANGES", "nova,neutron,cinder,glance,heat,magnum,manila,designate")),
		targetURL:    os.Getenv("TARGET_URL"),
		targetSecret: os.Getenv("TARGET_SECRET"),
		prefetch:     envInt("RABBITMQ_PREFETCH", 20),
		port:         envInt("PORT", 7476),
	}
	c.addresses = splitCSV(os.Getenv("RABBITMQ_ADDRESSES"))
	if c.targetURL == "" {
		return c, fmt.Errorf("TARGET_URL is required")
	}
	if c.targetSecret == "" {
		return c, fmt.Errorf("TARGET_SECRET is required")
	}
	if c.url == "" && len(c.addresses) == 0 {
		return c, fmt.Errorf("set RABBITMQ_URL or RABBITMQ_ADDRESSES")
	}
	if len(c.exchanges) == 0 {
		return c, fmt.Errorf("RABBITMQ_EXCHANGES is empty")
	}
	return c, nil
}

// dial connects using RABBITMQ_URL if set, else tries each host:port in RABBITMQ_ADDRESSES until
// one accepts the connection.
func dial(cfg config) (*amqp.Connection, error) {
	if cfg.url != "" {
		return amqp.Dial(cfg.url)
	}
	var lastErr error
	for _, addr := range cfg.addresses {
		conn, err := amqp.Dial(fmt.Sprintf("amqp://%s:%s@%s/", cfg.username, cfg.password, addr))
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("all rabbitmq addresses failed: %w", lastErr)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
