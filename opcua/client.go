// Package opcua wraps the gopcua client to provide a simple subscription loop.
package opcua

import (
	"context"
	"log"
	"math"
	"time"

	"github.com/gopcua/opcua"
	"github.com/gopcua/opcua/ua"

	"github.com/ahmadbass3l/opcua-railway-go/config"
	"github.com/ahmadbass3l/opcua-railway-go/sse"
)

// sensorMeta maps NodeId string → (sensor_id, unit).
var sensorMeta = map[string][2]string{
	"ns=2;i=1001": {"rail_temp_1", "°C"},
	"ns=2;i=1002": {"wheel_vibration_1", "mm/s"},
	"ns=2;i=1003": {"brake_pressure_1", "bar"},
}

func meta(nodeID string) (string, string) {
	if m, ok := sensorMeta[nodeID]; ok {
		return m[0], m[1]
	}
	return nodeID, ""
}

// Handler is called with each new Reading from the OPC UA server.
type Handler func(r sse.Reading)

// Run connects to the OPC UA server, creates a subscription, and calls h for
// every DataChangeNotification. Reconnects automatically with exponential back-off.
func Run(ctx context.Context, cfg config.Config, h Handler) {
	backoff := 2.0
	for {
		if ctx.Err() != nil {
			log.Println("OPC UA client: context cancelled, stopping")
			return
		}
		if err := runOnce(ctx, cfg, h); err != nil {
			log.Printf("OPC UA error: %v — reconnecting in %.0fs", err, backoff)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(backoff) * time.Second):
			}
			backoff = math.Min(backoff*2, 60)
		} else {
			backoff = 2.0
		}
	}
}

func runOnce(ctx context.Context, cfg config.Config, h Handler) error {
	// NewClient now returns (*Client, error) in v0.5.x
	c, err := opcua.NewClient(cfg.OpcuaEndpoint,
		opcua.SecurityMode(ua.MessageSecurityModeNone),
	)
	if err != nil {
		return err
	}
	if err := c.Connect(ctx); err != nil {
		return err
	}
	defer c.Close(ctx) //nolint:errcheck
	log.Printf("OPC UA connected: %s", cfg.OpcuaEndpoint)

	notifyCh := make(chan *opcua.PublishNotificationData, 128)
	sub, err := c.Subscribe(ctx, &opcua.SubscriptionParameters{
		Interval: time.Duration(cfg.OpcuaIntervalMs) * time.Millisecond,
	}, notifyCh)
	if err != nil {
		return err
	}
	defer sub.Cancel(ctx) //nolint:errcheck

	// Build a map: clientHandle (uint32 index) → nodeId string
	handleToNode := make(map[uint32]string)
	for i, nidStr := range cfg.OpcuaNodeIDs {
		nodeID, err := ua.ParseNodeID(nidStr)
		if err != nil {
			log.Printf("Invalid NodeId %q: %v", nidStr, err)
			continue
		}
		handle := uint32(i + 1) // client handles start at 1
		handleToNode[handle] = nidStr

		miReq := opcua.NewMonitoredItemCreateRequestWithDefaults(nodeID, ua.AttributeIDValue, handle)
		res, err := sub.Monitor(ctx, ua.TimestampsToReturnBoth, miReq)
		if err != nil {
			log.Printf("Monitor error for %s: %v", nidStr, err)
			continue
		}
		if res.Results[0].StatusCode != ua.StatusOK {
			log.Printf("Monitor status error for %s: %v", nidStr, res.Results[0].StatusCode)
			continue
		}
		log.Printf("Monitoring NodeId %s (handle %d)", nidStr, handle)
	}

	log.Printf("OPC UA subscription active (%d nodes @ %dms)",
		len(cfg.OpcuaNodeIDs), cfg.OpcuaIntervalMs)

	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-notifyCh:
			if !ok {
				return nil
			}
			if msg.Error != nil {
				return msg.Error
			}
			dcn, ok := msg.Value.(*ua.DataChangeNotification)
			if !ok {
				continue
			}
			for _, item := range dcn.MonitoredItems {
				// ClientHandle is uint32; look up the nodeId string
				nidStr, found := handleToNode[item.ClientHandle]
				if !found {
					continue
				}
				v := item.Value
				if v == nil || v.Value == nil {
					continue
				}
				val, ok := toFloat64(v.Value.Value())
				if !ok {
					continue
				}
				// quality: 0 means Good (ua.StatusOK == 0)
				quality := int(v.Status)
				if v.Status == ua.StatusOK {
					quality = 0
				}
				ts := v.SourceTimestamp
				if ts.IsZero() {
					ts = time.Now().UTC()
				}
				sensorID, unit := meta(nidStr)
				h(sse.Reading{
					SensorID: sensorID,
					NodeID:   nidStr,
					Value:    val,
					Unit:     unit,
					Quality:  quality,
					Time:     ts,
				})
			}
		}
	}
}

func toFloat64(v interface{}) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int:
		return float64(t), true
	case int16:
		return float64(t), true
	case int32:
		return float64(t), true
	case int64:
		return float64(t), true
	case uint16:
		return float64(t), true
	case uint32:
		return float64(t), true
	case uint64:
		return float64(t), true
	}
	return 0, false
}
