package notifier

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/smtp"
	"strings"
	"sync"
	"time"

	"github.com/metabinary-ltd/storagesentinel/internal/config"
	"github.com/metabinary-ltd/storagesentinel/internal/storage"
	"github.com/metabinary-ltd/storagesentinel/internal/types"
)

type Notifier struct {
	store       *storage.Store
	cfg         config.NotificationsConfig
	debounce    time.Duration
	minSeverity string
	lastSent    map[string]time.Time
	mu          sync.Mutex
	client      *http.Client
	logger      *slog.Logger
	stopChan    chan struct{}
	wg          sync.WaitGroup
}

func New(store *storage.Store, cfg config.NotificationsConfig, debounce time.Duration, minSeverity string, logger *slog.Logger) *Notifier {
	return &Notifier{
		store:       store,
		cfg:         cfg,
		debounce:    debounce,
		minSeverity: strings.ToLower(minSeverity),
		lastSent:    make(map[string]time.Time),
		client:      &http.Client{Timeout: 10 * time.Second},
		logger:      logger,
		stopChan:    make(chan struct{}),
	}
}

// Start begins the background worker that processes the notification queue
func (n *Notifier) Start(ctx context.Context) {
	n.wg.Add(1)
	go n.processQueue(ctx)
}

// Stop stops the background worker
func (n *Notifier) Stop() {
	close(n.stopChan)
	n.wg.Wait()
}

// Send queues notifications for all configured channels
// Callers don't need to know which channels are configured
func (n *Notifier) Send(ctx context.Context, alerts []types.Alert) {
	for _, alert := range alerts {
		if !n.allowed(alert.Severity) {
			continue
		}

		// Check debounce
		key := alert.SourceType + ":" + alert.SourceID + ":" + alert.Subject
		if n.isDebounced(key, alert.Timestamp) {
			continue
		}

		// Store alert first
		alertID, err := n.store.AddAlert(ctx, storage.Alert{
			Severity:   alert.Severity,
			SourceType: alert.SourceType,
			SourceID:   alert.SourceID,
			Subject:    alert.Subject,
			Message:    alert.Message,
			Timestamp:  alert.Timestamp,
		})
		if err != nil {
			n.logger.Warn("failed to store alert", "error", err)
			continue
		}

		// Queue for each enabled channel
		if n.cfg.Email.Enabled {
			if err := n.store.EnqueueNotification(ctx, alertID, "email"); err != nil {
				n.logger.Warn("failed to queue email notification", "error", err)
			}
		}

		for _, webhook := range n.cfg.Webhooks {
			if webhook.URL != "" {
				if err := n.store.EnqueueNotification(ctx, alertID, "webhook:"+webhook.Name); err != nil {
					n.logger.Warn("failed to queue webhook notification", "webhook", webhook.Name, "error", err)
				}
			}
		}

		n.markSent(key, alert.Timestamp)
	}
}

// GetUnsentCount returns the number of unsent notifications
func (n *Notifier) GetUnsentCount(ctx context.Context) (int, error) {
	return n.store.GetUnsentNotificationCount(ctx)
}

func (n *Notifier) allowed(sev string) bool {
	order := map[string]int{"info": 1, "warning": 2, "critical": 3}
	return order[strings.ToLower(sev)] >= order[n.minSeverity]
}

func (n *Notifier) isDebounced(key string, ts int64) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	last, ok := n.lastSent[key]
	if !ok {
		return false
	}
	return time.Unix(ts, 0).Sub(last) < n.debounce
}

func (n *Notifier) markSent(key string, ts int64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.lastSent[key] = time.Unix(ts, 0)
}

// processQueue is the background worker that processes queued notifications
func (n *Notifier) processQueue(ctx context.Context) {
	defer n.wg.Done()

	ticker := time.NewTicker(30 * time.Second) // Check queue every 30 seconds
	defer ticker.Stop()

	for {
		select {
		case <-n.stopChan:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			n.processPendingNotifications(ctx)
		}
	}
}

func (n *Notifier) processPendingNotifications(ctx context.Context) {
	entries, err := n.store.GetPendingNotifications(ctx, 50)
	if err != nil {
		n.logger.Warn("failed to get pending notifications", "error", err)
		return
	}

	for _, entry := range entries {
		alert, err := n.store.GetAlert(ctx, entry.AlertID)
		if err != nil || alert == nil {
			n.logger.Warn("failed to get alert for notification", "queue_id", entry.ID, "error", err)
			continue
		}

		alertType := types.Alert{
			ID:         alert.ID,
			Timestamp:  alert.Timestamp,
			Severity:   alert.Severity,
			SourceType: alert.SourceType,
			SourceID:   alert.SourceID,
			Subject:    alert.Subject,
			Message:    alert.Message,
		}

		var sendErr error
		if strings.HasPrefix(entry.Channel, "webhook:") {
			webhookName := strings.TrimPrefix(entry.Channel, "webhook:")
			sendErr = n.sendWebhook(ctx, alertType, webhookName)
		} else if entry.Channel == "email" {
			sendErr = n.sendEmail(ctx, alertType)
		}

		if sendErr != nil {
			// Calculate next retry with exponential backoff
			nextRetry := n.calculateNextRetry(entry.Attempts)
			if err := n.store.MarkNotificationFailed(ctx, entry.ID, sendErr.Error(), nextRetry); err != nil {
				n.logger.Warn("failed to mark notification as failed", "queue_id", entry.ID, "error", err)
			}
			n.logger.Warn("notification send failed", "channel", entry.Channel, "attempts", entry.Attempts, "error", sendErr)
		} else {
			if err := n.store.MarkNotificationSent(ctx, entry.ID); err != nil {
				n.logger.Warn("failed to mark notification as sent", "queue_id", entry.ID, "error", err)
			}
			n.logger.Debug("notification sent", "channel", entry.Channel, "alert", alert.Subject)
		}
	}
}

func (n *Notifier) calculateNextRetry(attempts int) time.Time {
	// Exponential backoff: 1min, 5min, 15min, 1hr, 6hr, 24hr
	backoffs := []time.Duration{
		1 * time.Minute,
		5 * time.Minute,
		15 * time.Minute,
		1 * time.Hour,
		6 * time.Hour,
		24 * time.Hour,
	}
	
	idx := attempts
	if idx >= len(backoffs) {
		idx = len(backoffs) - 1
	}
	
	return time.Now().Add(backoffs[idx])
}

func (n *Notifier) sendEmail(ctx context.Context, alert types.Alert) error {
	if !n.cfg.Email.Enabled || len(n.cfg.Email.To) == 0 {
		return fmt.Errorf("email not configured")
	}

	subject := fmt.Sprintf("[%s] Storage Sentinel: %s", strings.ToUpper(alert.Severity), alert.Subject)
	body := fmt.Sprintf(`Storage Sentinel Alert

Severity: %s
Source: %s (%s)
Subject: %s

%s

Timestamp: %s
`, alert.Severity, alert.SourceType, alert.SourceID, alert.Subject, alert.Message,
		time.Unix(alert.Timestamp, 0).Format(time.RFC3339))

	msg := []byte(fmt.Sprintf("From: %s\r\n", n.cfg.Email.From) +
		fmt.Sprintf("To: %s\r\n", strings.Join(n.cfg.Email.To, ",")) +
		fmt.Sprintf("Subject: %s\r\n", subject) +
		"Content-Type: text/plain; charset=UTF-8\r\n" +
		"\r\n" +
		body)

	addr := fmt.Sprintf("%s:%d", n.cfg.Email.SMTPServer, n.cfg.Email.SMTPPort)
	
	var auth smtp.Auth
	if n.cfg.Email.Username != "" && n.cfg.Email.Password != "" {
		auth = smtp.PlainAuth("", n.cfg.Email.Username, n.cfg.Email.Password, n.cfg.Email.SMTPServer)
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- smtp.SendMail(addr, auth, n.cfg.Email.From, n.cfg.Email.To, msg)
	}()

	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (n *Notifier) sendWebhook(ctx context.Context, alert types.Alert, webhookName string) error {
	var webhookURL string
	for _, w := range n.cfg.Webhooks {
		if w.Name == webhookName && w.URL != "" {
			webhookURL = w.URL
			break
		}
	}

	if webhookURL == "" {
		return fmt.Errorf("webhook not found: %s", webhookName)
	}

	payload, err := json.Marshal(alert)
	if err != nil {
		return fmt.Errorf("marshal alert: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned status %d", resp.StatusCode)
	}

	return nil
}

