package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/metabinary-ltd/storagesentinel/internal/storage"
)

func (s *Server) registerRoutes() {
	s.mux.HandleFunc("/health", s.handleHealth)
	s.mux.HandleFunc("/api/v1/summary", s.wrapAuth(s.handleSummary))
	s.mux.HandleFunc("/api/v1/disks", s.wrapAuth(s.handleDisks))
	s.mux.HandleFunc("/api/v1/pools", s.wrapAuth(s.handlePools))
	s.mux.HandleFunc("/api/v1/alerts", s.wrapAuth(s.handleAlerts))
	s.mux.HandleFunc("/api/v1/collect/smart", s.wrapAuth(s.handleCollectSmart))
	s.mux.HandleFunc("/api/v1/collect/nvme", s.wrapAuth(s.handleCollectNvme))
	s.mux.HandleFunc("/api/v1/collect/zfs", s.wrapAuth(s.handleCollectZfs))
	s.mux.HandleFunc("/api/v1/notifications/queue", s.wrapAuth(s.handleNotificationQueue))
	s.mux.HandleFunc("/api/v1/pools/", s.wrapAuth(s.handlePoolRoutes))
}

func (s *Server) wrapAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.authToken != "" && !strings.HasPrefix(r.URL.Path, "/health") {
			auth := r.Header.Get("Authorization")
			if auth != "Bearer "+s.authToken {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
		}
		next(w, r)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	report, err := s.health.Summary(r.Context())
	if err != nil {
		s.logger.Error("failed to build summary", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) handleDisks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, nil)
		return
	}
	// detail route: /api/v1/disks/{id}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v1/disks"), "/")
	if len(parts) > 1 && parts[1] != "" {
		id := strings.TrimPrefix(r.URL.Path, "/api/v1/disks/")
		disk, _ := s.store.GetDisk(r.Context(), id)
		if disk == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		resp := map[string]interface{}{
			"disk": disk,
		}
		if disk.Type == "nvme" {
			hist, _ := s.store.NvmeHistory(r.Context(), id, 10)
			resp["history"] = hist
		} else {
			hist, _ := s.store.SmartHistory(r.Context(), id, 10)
			resp["history"] = hist
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	disks, err := s.store.ListDisks(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	writeJSON(w, http.StatusOK, disks)
}

func (s *Server) handlePools(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, nil)
		return
	}

	// Only handle listing - detail routes are handled by handlePoolRoutes
	pools, err := s.store.ListPools(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	writeJSON(w, http.StatusOK, pools)
}

func (s *Server) handlePoolDetail(w http.ResponseWriter, r *http.Request, poolName string) {
	// Get pool status
	pools, err := s.store.ListPools(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}

	var pool *storage.PoolStatus
	for _, p := range pools {
		if p.Name == poolName {
			pool = &p
			break
		}
	}

	if pool == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "pool not found"})
		return
	}

	// Get device mappings
	devices, _ := s.store.GetPoolDevices(r.Context(), poolName)

	// Get scrub history
	scrubHistory, _ := s.store.GetScrubHistory(r.Context(), poolName, 20)

	resp := map[string]interface{}{
		"pool":          pool,
		"devices":       devices,
		"scrub_history": scrubHistory,
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleAlerts(w http.ResponseWriter, r *http.Request) {
	// Check if this is an acknowledge route: /api/v1/alerts/{id}/acknowledge
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/alerts/")
	parts := strings.Split(path, "/")

	if len(parts) >= 2 && parts[1] == "acknowledge" {
		// Parse alert ID
		alertID, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid alert ID"})
			return
		}
		s.handleAcknowledgeAlert(w, r, alertID)
		return
	}

	// Default: list alerts
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, nil)
		return
	}

	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	alerts, err := s.store.RecentAlerts(r.Context(), limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	writeJSON(w, http.StatusOK, alerts)
}

func (s *Server) handleAcknowledgeAlert(w http.ResponseWriter, r *http.Request, alertID int64) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, nil)
		return
	}

	if err := s.store.AcknowledgeAlert(r.Context(), alertID); err != nil {
		if err.Error() == "alert not found" {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "alert not found"})
			return
		}
		s.logger.Error("failed to acknowledge alert", "alert_id", alertID, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to acknowledge alert"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":   "acknowledged",
		"alert_id": alertID,
	})
}

func (s *Server) handleCollectSmart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, nil)
		return
	}
	if s.triggers.CollectSmart != nil {
		_ = s.triggers.CollectSmart(r.Context())
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "triggered"})
}

func (s *Server) handleCollectNvme(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, nil)
		return
	}
	if s.triggers.CollectNvme != nil {
		_ = s.triggers.CollectNvme(r.Context())
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "triggered"})
}

func (s *Server) handleCollectZfs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, nil)
		return
	}
	if s.triggers.CollectZfs != nil {
		_ = s.triggers.CollectZfs(r.Context())
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "triggered"})
}

func (s *Server) handlePoolRoutes(w http.ResponseWriter, r *http.Request) {
	// Handle routes like /api/v1/pools/{name} and /api/v1/pools/{name}/scrub
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/pools/")
	parts := strings.Split(path, "/")

	if len(parts) == 0 || parts[0] == "" {
		// Fall back to list pools
		s.handlePools(w, r)
		return
	}

	poolName := parts[0]

	// Check if it's a scrub endpoint
	if len(parts) >= 2 && parts[1] == "scrub" {
		s.handlePoolScrub(w, r, poolName)
		return
	}

	// Otherwise it's a detail endpoint
	s.handlePoolDetail(w, r, poolName)
}

func (s *Server) handlePoolScrub(w http.ResponseWriter, r *http.Request, poolName string) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, nil)
		return
	}

	if s.triggers.TriggerScrub != nil {
		if err := s.triggers.TriggerScrub(r.Context(), poolName); err != nil {
			s.logger.Error("failed to trigger scrub", "pool", poolName, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to trigger scrub"})
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "scrub triggered", "pool": poolName})
	} else {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "scrub trigger not configured"})
	}
}

func (s *Server) handleNotificationQueue(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, nil)
		return
	}

	if s.notifier == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"unsent_count": 0,
		})
		return
	}

	count, err := s.notifier.GetUnsentCount(r.Context())
	if err != nil {
		s.logger.Error("failed to get unsent notification count", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"unsent_count": count,
	})
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}
