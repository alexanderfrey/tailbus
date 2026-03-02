package daemon

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics exports Prometheus metrics for the daemon. It reads counters directly
// from ActivityBus atomics at scrape time (no double-counting), and provides
// histograms for routing duration and session lifetime.
type Metrics struct {
	activity *ActivityBus

	// Descriptors for ActivityBus counters (collected at scrape time)
	messagesRoutedDesc        *prometheus.Desc
	messagesDeliveredLocalDesc *prometheus.Desc
	messagesSentRemoteDesc    *prometheus.Desc
	messagesReceivedRemoteDesc *prometheus.Desc
	sessionsOpenedDesc        *prometheus.Desc
	sessionsResolvedDesc      *prometheus.Desc

	// Histograms (observed explicitly)
	MessageRoutingDuration *prometheus.Histogram
	SessionLifetime        *prometheus.Histogram
}

// NewMetrics creates a new Metrics instance backed by the given ActivityBus.
func NewMetrics(activity *ActivityBus) *Metrics {
	routingDuration := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "tailbus_message_routing_duration_seconds",
		Help:    "Time taken to route a message.",
		Buckets: prometheus.DefBuckets,
	})
	sessionLifetime := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "tailbus_session_lifetime_seconds",
		Help:    "Duration from session open to resolve.",
		Buckets: []float64{0.1, 0.5, 1, 5, 10, 30, 60, 300, 600, 3600},
	})

	return &Metrics{
		activity: activity,

		messagesRoutedDesc:         prometheus.NewDesc("tailbus_messages_routed_total", "Total messages routed.", nil, nil),
		messagesDeliveredLocalDesc: prometheus.NewDesc("tailbus_messages_delivered_local_total", "Total messages delivered locally.", nil, nil),
		messagesSentRemoteDesc:     prometheus.NewDesc("tailbus_messages_sent_remote_total", "Total messages sent to remote peers.", nil, nil),
		messagesReceivedRemoteDesc: prometheus.NewDesc("tailbus_messages_received_remote_total", "Total messages received from remote peers.", nil, nil),
		sessionsOpenedDesc:         prometheus.NewDesc("tailbus_sessions_opened_total", "Total sessions opened.", nil, nil),
		sessionsResolvedDesc:       prometheus.NewDesc("tailbus_sessions_resolved_total", "Total sessions resolved.", nil, nil),

		MessageRoutingDuration: &routingDuration,
		SessionLifetime:        &sessionLifetime,
	}
}

// Describe implements prometheus.Collector.
func (m *Metrics) Describe(ch chan<- *prometheus.Desc) {
	ch <- m.messagesRoutedDesc
	ch <- m.messagesDeliveredLocalDesc
	ch <- m.messagesSentRemoteDesc
	ch <- m.messagesReceivedRemoteDesc
	ch <- m.sessionsOpenedDesc
	ch <- m.sessionsResolvedDesc
	(*m.MessageRoutingDuration).Describe(ch)
	(*m.SessionLifetime).Describe(ch)
}

// Collect implements prometheus.Collector.
func (m *Metrics) Collect(ch chan<- prometheus.Metric) {
	ch <- prometheus.MustNewConstMetric(m.messagesRoutedDesc, prometheus.CounterValue, float64(m.activity.MessagesRouted.Load()))
	ch <- prometheus.MustNewConstMetric(m.messagesDeliveredLocalDesc, prometheus.CounterValue, float64(m.activity.MessagesDeliveredLocal.Load()))
	ch <- prometheus.MustNewConstMetric(m.messagesSentRemoteDesc, prometheus.CounterValue, float64(m.activity.MessagesSentRemote.Load()))
	ch <- prometheus.MustNewConstMetric(m.messagesReceivedRemoteDesc, prometheus.CounterValue, float64(m.activity.MessagesReceivedRemote.Load()))
	ch <- prometheus.MustNewConstMetric(m.sessionsOpenedDesc, prometheus.CounterValue, float64(m.activity.SessionsOpened.Load()))
	ch <- prometheus.MustNewConstMetric(m.sessionsResolvedDesc, prometheus.CounterValue, float64(m.activity.SessionsResolved.Load()))
	(*m.MessageRoutingDuration).Collect(ch)
	(*m.SessionLifetime).Collect(ch)
}

// Serve starts an HTTP server exposing /metrics on the given address.
// It blocks until the context is cancelled, then shuts down gracefully.
func (m *Metrics) Serve(ctx context.Context, addr string, logger *slog.Logger) {
	reg := prometheus.NewRegistry()
	reg.MustRegister(m)

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))

	srv := &http.Server{Addr: addr, Handler: mux}

	go func() {
		<-ctx.Done()
		srv.Close()
	}()

	logger.Info("metrics server listening", "addr", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("metrics server error", "error", err)
	}
}
