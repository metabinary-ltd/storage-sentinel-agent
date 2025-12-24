package uplink

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/metabinary-ltd/storagesentinel/internal/types"
)

type Client struct {
	endpoint string
	token    string
	hostID   string
	hostname string
	client   *http.Client
}

type RegisterRequest struct {
	Hostname    string `json:"hostname"`
	OSInfo      string `json:"os_info,omitempty"`
	AgentVersion string `json:"agent_version,omitempty"`
}

type RegisterResponse struct {
	HostID string `json:"host_id"`
}

type SnapshotPayload struct {
	HostID      string                 `json:"host_id"`
	Timestamp   int64                  `json:"timestamp"`
	Disks       []types.Disk           `json:"disks,omitempty"`
	Pools       []types.PoolStatus    `json:"pools,omitempty"`
	SmartSnaps  []types.SmartSnapshot `json:"smart_snapshots,omitempty"`
	NvmeSnaps   []types.NvmeSnapshot  `json:"nvme_snapshots,omitempty"`
	HealthReport *types.HealthReport   `json:"health_report,omitempty"`
}

type Command struct {
	ID          string          `json:"id"`
	Type        string          `json:"type"`
	Params      json.RawMessage `json:"params,omitempty"`
	CreatedAt   int64           `json:"created_at"`
}

type CommandResponse struct {
	Commands []Command `json:"commands"`
}

type Schedule struct {
	ID           string `json:"id"`
	TaskType     string `json:"task_type"`
	ScheduleType string `json:"schedule_type"`
	ScheduleValue string `json:"schedule_value"`
	Enabled      bool   `json:"enabled"`
	UpdatedAt    int64  `json:"updated_at"`
}

type ScheduleResponse struct {
	Schedules []Schedule `json:"schedules"`
}

func New(endpoint, token, hostID, hostname string) *Client {
	return &Client{
		endpoint: strings.TrimSuffix(endpoint, "/"),
		token:    token,
		hostID:   hostID,
		hostname: hostname,
		client:   &http.Client{Timeout: 30 * time.Second},
	}
}

// SetHostID updates the host ID after registration
func (c *Client) SetHostID(hostID string) {
	c.hostID = hostID
}

// RegisterHost registers this agent with the cloud dashboard
func (c *Client) RegisterHost(ctx context.Context, osInfo, agentVersion string) (string, error) {
	payload := RegisterRequest{
		Hostname:     c.hostname,
		OSInfo:       osInfo,
		AgentVersion: agentVersion,
	}
	
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/api/v1/agent/register", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("registration failed: status %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	var regResp RegisterResponse
	if err := json.NewDecoder(resp.Body).Decode(&regResp); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	c.hostID = regResp.HostID
	return regResp.HostID, nil
}

// SendSummary sends a health summary report (backward compatible)
func (c *Client) SendSummary(ctx context.Context, report types.HealthReport) error {
	return c.sendWithRetry(ctx, "/api/v1/agent/ingest", report, 3)
}

// SendFullSnapshot sends detailed snapshot data including disk/pool info and snapshots
func (c *Client) SendFullSnapshot(ctx context.Context, payload SnapshotPayload) error {
	payload.HostID = c.hostID
	return c.sendWithRetry(ctx, "/api/v1/agent/snapshot", payload, 3)
}

// PollCommands checks for pending remote commands from the cloud dashboard
func (c *Client) PollCommands(ctx context.Context) ([]Command, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/api/v1/agent/commands", nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.hostID != "" {
		req.Header.Set("X-Host-ID", c.hostID)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// No commands available
		return []Command{}, nil
	}

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("poll commands failed: status %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	var cmdResp CommandResponse
	if err := json.NewDecoder(resp.Body).Decode(&cmdResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return cmdResp.Commands, nil
}

// AcknowledgeCommand marks a command as executed
func (c *Client) AcknowledgeCommand(ctx context.Context, commandID string, success bool, errorMsg string) error {
	payload := map[string]interface{}{
		"success": success,
	}
	if errorMsg != "" {
		payload["error"] = errorMsg
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, 
		c.endpoint+"/api/v1/agent/commands/"+commandID+"/ack", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.hostID != "" {
		req.Header.Set("X-Host-ID", c.hostID)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("acknowledge command failed: status %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	return nil
}

// PollSchedules fetches schedules from the cloud dashboard
func (c *Client) PollSchedules(ctx context.Context) ([]Schedule, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/api/v1/agent/schedules", nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.hostID != "" {
		req.Header.Set("X-Host-ID", c.hostID)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// No schedules available
		return []Schedule{}, nil
	}

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("poll schedules failed: status %d, body: %s", resp.StatusCode, string(bodyBytes))
	}

	var schedResp ScheduleResponse
	if err := json.NewDecoder(resp.Body).Decode(&schedResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return schedResp.Schedules, nil
}

// sendWithRetry sends a request with exponential backoff retry
func (c *Client) sendWithRetry(ctx context.Context, path string, payload interface{}, maxRetries int) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	var lastErr error
	backoff := time.Second

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
				backoff *= 2 // Exponential backoff
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+path, bytes.NewReader(body))
		if err != nil {
			lastErr = fmt.Errorf("create request: %w", err)
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		if c.hostID != "" {
			req.Header.Set("X-Host-ID", c.hostID)
		}

		resp, err := c.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("send request: %w", err)
			continue
		}
		resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}

		lastErr = fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	return lastErr
}
