// Package sse implements a fan-out SSE broker.
// Each call to Subscribe returns a channel that receives Reading events.
// Publish sends a Reading to every subscribed channel.
package sse

import (
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Reading is the canonical event emitted by the OPC UA subscription handler.
type Reading struct {
	SensorID string    `json:"sensor_id"`
	NodeID   string    `json:"node_id"`
	Value    float64   `json:"value"`
	Unit     string    `json:"unit"`
	Quality  int       `json:"quality"`
	Time     time.Time `json:"time"`
}

// ToSSEFrame formats a Reading as a Server-Sent Event frame.
func (r Reading) ToSSEFrame() string {
	b, _ := json.Marshal(r)
	return fmt.Sprintf("event: reading\ndata: %s\n\n", b)
}

const clientChanSize = 256

// Broker fans out Readings to all connected SSE clients.
type Broker struct {
	mu      sync.RWMutex
	clients map[string]chan Reading
}

// NewBroker creates an initialised Broker.
func NewBroker() *Broker {
	return &Broker{clients: make(map[string]chan Reading)}
}

// Subscribe registers a new SSE client and returns its ID and receive channel.
func (b *Broker) Subscribe() (string, <-chan Reading) {
	id := uuid.New().String()
	ch := make(chan Reading, clientChanSize)
	b.mu.Lock()
	b.clients[id] = ch
	b.mu.Unlock()
	log.Printf("SSE client connected: %s  (total: %d)", id, b.ClientCount())
	return id, ch
}

// Unsubscribe removes the client and closes its channel.
func (b *Broker) Unsubscribe(id string) {
	b.mu.Lock()
	if ch, ok := b.clients[id]; ok {
		close(ch)
		delete(b.clients, id)
	}
	b.mu.Unlock()
	log.Printf("SSE client disconnected: %s  (total: %d)", id, b.ClientCount())
}

// Publish sends a Reading to every subscribed client.
// Slow clients: if the channel is full the oldest reading is dropped.
func (b *Broker) Publish(r Reading) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.clients {
		select {
		case ch <- r:
		default:
			// Channel full — drain one, then send
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- r:
			default:
			}
		}
	}
}

// ClientCount returns the number of currently connected SSE clients.
func (b *Broker) ClientCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.clients)
}
