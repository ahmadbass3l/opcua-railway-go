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
// In a real deployment these come from the OPC UA address space (BrowseName +
// EUInformation). Here they are hardcoded for the scaffold.
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
// every DataChangeNotification. It reconnects automatically on error.
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
	c := opcua.NewClient(cfg.OpcuaEndpoint, opcua.SecurityModeNone())
	if err := c.Connect(ctx); err != nil {
		return err
	}
	defer c.CloseWithContext(ctx) //nolint:errcheck
	log.Printf("OPC UA connected: %s", cfg.OpcuaEndpoint)

	notifyCh := make(chan *opcua.PublishNotificationData, 128)
	sub, err := c.SubscribeWithContext(ctx, &opcua.SubscriptionParameters{
		Interval: time.Duration(cfg.OpcuaIntervalMs) * time.Millisecond,
	}, notifyCh)
	if err != nil {
		return err
	}
	defer sub.CancelWithContext(ctx) //nolint:errcheck

	// Add one MonitoredItem per configured NodeId
	for _, nidStr := range cfg.OpcuaNodeIDs {
		nodeID, err := ua.ParseNodeID(nidStr)
		if err != nil {
			log.Printf("Invalid NodeId %q: %v", nidStr, err)
			continue
		}
		miReq := opcua.NewMonitoredItemCreateRequestWithDefaults(nodeID, ua.AttributeIDValue, nidStr)
		res, err := sub.Monitor(ua.TimestampsToReturnBoth, miReq)
		if err != nil || res.Results[0].StatusCode != ua.StatusOK {
			log.Printf("Failed to monitor %s: %v %v", nidStr, err, res.Results[0].StatusCode)
			continue
		}
		log.Printf("Monitoring NodeId %s", nidStr)
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
				nidStr, _ := item.ClientHandle.(string)
				v := item.Value
				if v == nil || v.Value == nil {
					continue
				}
				val, ok := toFloat64(v.Value.Value())
				if !ok {
					continue
				}
				quality := 0
				if !v.Status.OK() {
					quality = int(v.Status)
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
	case int32:
		return float64(t), true
	case int64:
		return float64(t), true
	case uint32:
		return float64(t), true
	case uint64:
		return float64(t), true
	}
	return 0, false
}
