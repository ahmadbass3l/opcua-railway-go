// Package db provides a TimescaleDB writer and query helpers via pgx/v5.
package db

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ahmadbass3l/opcua-railway-go/sse"
)

// Pool is the shared connection pool; initialised by Init().
var Pool *pgxpool.Pool

// Init creates the pgxpool from dsn.
func Init(ctx context.Context, dsn string) error {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	if err := pool.Ping(ctx); err != nil {
		return err
	}
	Pool = pool
	log.Println("DB pool created")
	return nil
}

// Close shuts down the pool.
func Close() {
	if Pool != nil {
		Pool.Close()
		log.Println("DB pool closed")
	}
}

// InsertReading writes a single sensor reading to the hypertable.
func InsertReading(ctx context.Context, r sse.Reading) error {
	_, err := Pool.Exec(ctx,
		`INSERT INTO sensor_readings (time, sensor_id, node_id, value, unit, quality)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		r.Time, r.SensorID, r.NodeID, r.Value, r.Unit, r.Quality,
	)
	return err
}

// QueryReadingsParams holds filters for the historical query.
type QueryReadingsParams struct {
	SensorID string
	From     time.Time
	To       time.Time
	Limit    int
}

// ReadingRow is a single row returned from sensor_readings.
type ReadingRow struct {
	Time     time.Time `json:"time"`
	SensorID string    `json:"sensor_id"`
	NodeID   string    `json:"node_id"`
	Value    float64   `json:"value"`
	Unit     string    `json:"unit"`
	Quality  int16     `json:"quality"`
}

// QueryReadings returns raw readings filtered by sensor and time range.
func QueryReadings(ctx context.Context, p QueryReadingsParams) ([]ReadingRow, error) {
	if p.Limit <= 0 || p.Limit > 50000 {
		p.Limit = 5000
	}
	rows, err := Pool.Query(ctx,
		`SELECT time, sensor_id, node_id, value, unit, quality
		 FROM sensor_readings
		 WHERE sensor_id = $1 AND time >= $2 AND time <= $3
		 ORDER BY time ASC
		 LIMIT $4`,
		p.SensorID, p.From, p.To, p.Limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []ReadingRow
	for rows.Next() {
		var r ReadingRow
		if err := rows.Scan(&r.Time, &r.SensorID, &r.NodeID, &r.Value, &r.Unit, &r.Quality); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AggRow is one row from the continuous aggregate view.
type AggRow struct {
	Bucket   time.Time `json:"bucket"`
	SensorID string    `json:"sensor_id"`
	AvgValue float64   `json:"avg_value"`
	MinValue float64   `json:"min_value"`
	MaxValue float64   `json:"max_value"`
}

// QueryAggregate queries the readings_1min continuous aggregate view.
func QueryAggregate(ctx context.Context, sensorID string, from, to time.Time) ([]AggRow, error) {
	rows, err := Pool.Query(ctx,
		`SELECT bucket, sensor_id, avg_value, min_value, max_value
		 FROM readings_1min
		 WHERE sensor_id = $1 AND bucket >= $2 AND bucket <= $3
		 ORDER BY bucket ASC`,
		sensorID, from, to,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []AggRow
	for rows.Next() {
		var r AggRow
		if err := rows.Scan(&r.Bucket, &r.SensorID, &r.AvgValue, &r.MinValue, &r.MaxValue); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListSensors returns all distinct sensor IDs in the database.
func ListSensors(ctx context.Context) ([]string, error) {
	rows, err := Pool.Query(ctx,
		`SELECT DISTINCT sensor_id FROM sensor_readings ORDER BY sensor_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		ids = append(ids, s)
	}
	return ids, rows.Err()
}

// HealthCheck pings the database.
func HealthCheck(ctx context.Context) bool {
	if Pool == nil {
		return false
	}
	return Pool.Ping(ctx) == nil
}
