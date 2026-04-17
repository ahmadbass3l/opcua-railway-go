// Package api contains all HTTP handlers.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/ahmadbass3l/opcua-railway-go/db"
	"github.com/ahmadbass3l/opcua-railway-go/sse"
)

// writeJSON serialises v as JSON and writes it with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// parseTime parses an ISO-8601 time query param, returning fallback on error/empty.
func parseTime(s string, fallback time.Time) time.Time {
	if s == "" {
		return fallback
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return fallback
	}
	return t
}

// ── /stream ──────────────────────────────────────────────────────────────────

// HandleStream serves the SSE live feed.
func HandleStream(broker *sse.Broker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)

		// Send an initial comment so the browser EventSource knows it's connected
		fmt.Fprint(w, ": connected\n\n")
		flusher.Flush()

		id, ch := broker.Subscribe()
		defer broker.Unsubscribe(id)

		keepAlive := time.NewTicker(15 * time.Second)
		defer keepAlive.Stop()

		for {
			select {
			case <-r.Context().Done():
				return
			case reading, ok := <-ch:
				if !ok {
					return
				}
				fmt.Fprint(w, reading.ToSSEFrame())
				flusher.Flush()
			case <-keepAlive.C:
				fmt.Fprint(w, ": keep-alive\n\n")
				flusher.Flush()
			}
		}
	}
}

// ── /readings ─────────────────────────────────────────────────────────────────

// HandleReadings serves historical raw readings.
func HandleReadings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	q := r.URL.Query()
	sensorID := q.Get("sensor_id")
	if sensorID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sensor_id is required"})
		return
	}

	now := time.Now().UTC()
	from := parseTime(q.Get("from"), now.Add(-time.Hour))
	to := parseTime(q.Get("to"), now)

	limit := 5000
	if s := q.Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			limit = n
		}
	}

	rows, err := db.QueryReadings(r.Context(), db.QueryReadingsParams{
		SensorID: sensorID,
		From:     from,
		To:       to,
		Limit:    limit,
	})
	if err != nil {
		log.Printf("QueryReadings error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if rows == nil {
		rows = []db.ReadingRow{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sensor_id": sensorID,
		"from":      from.Format(time.RFC3339),
		"to":        to.Format(time.RFC3339),
		"count":     len(rows),
		"readings":  rows,
	})
}

// ── /readings/aggregate ───────────────────────────────────────────────────────

// HandleAggregate serves pre-aggregated 1-min rollups from the continuous aggregate view.
func HandleAggregate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	q := r.URL.Query()
	sensorID := q.Get("sensor_id")
	if sensorID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sensor_id is required"})
		return
	}
	bucket := q.Get("bucket")
	if bucket == "" {
		bucket = "1min"
	}
	if bucket != "1min" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "supported buckets: 1min"})
		return
	}

	now := time.Now().UTC()
	from := parseTime(q.Get("from"), now.Add(-time.Hour))
	to := parseTime(q.Get("to"), now)

	rows, err := db.QueryAggregate(r.Context(), sensorID, from, to)
	if err != nil {
		log.Printf("QueryAggregate error: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if rows == nil {
		rows = []db.AggRow{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sensor_id": sensorID,
		"bucket":    bucket,
		"from":      from.Format(time.RFC3339),
		"to":        to.Format(time.RFC3339),
		"count":     len(rows),
		"data":      rows,
	})
}

// ── /sensors ──────────────────────────────────────────────────────────────────

// HandleSensors lists all distinct sensor IDs in the DB.
func HandleSensors(w http.ResponseWriter, r *http.Request) {
	ids, err := db.ListSensors(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if ids == nil {
		ids = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sensors": ids})
}

// ── /health ───────────────────────────────────────────────────────────────────

// HandleHealth returns service health.
func HandleHealth(broker *sse.Broker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		dbOK := db.HealthCheck(context.Background())
		status := http.StatusOK
		state := "ok"
		if !dbOK {
			status = http.StatusServiceUnavailable
			state = "degraded"
		}
		writeJSON(w, status, map[string]any{
			"status":      state,
			"db":          dbOK,
			"sse_clients": broker.ClientCount(),
		})
	}
}
