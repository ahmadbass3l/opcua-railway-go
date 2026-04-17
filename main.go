package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ahmadbass3l/opcua-railway-go/api"
	"github.com/ahmadbass3l/opcua-railway-go/config"
	"github.com/ahmadbass3l/opcua-railway-go/db"
	opcuaclient "github.com/ahmadbass3l/opcua-railway-go/opcua"
	"github.com/ahmadbass3l/opcua-railway-go/sse"
)

func main() {
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	cfg := config.Load()
	broker := sse.NewBroker()

	// ── Database ──────────────────────────────────────────────────────────────
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := db.Init(ctx, cfg.DBDSN); err != nil {
		log.Fatalf("DB init failed: %v", err)
	}
	defer db.Close()

	// ── OPC UA subscription (runs in background goroutine) ────────────────────
	go opcuaclient.Run(ctx, cfg, func(r sse.Reading) {
		// Fan out to SSE broker
		broker.Publish(r)

		// Write to DB (fire-and-forget with a short deadline)
		dbCtx, dbCancel := context.WithTimeout(ctx, 5*time.Second)
		defer dbCancel()
		if err := db.InsertReading(dbCtx, r); err != nil {
			log.Printf("DB insert error: %v", err)
		}
	})

	// ── HTTP routes ───────────────────────────────────────────────────────────
	mux := http.NewServeMux()
	mux.Handle("/stream",             api.HandleStream(broker))
	mux.HandleFunc("/readings",       api.HandleReadings)
	mux.HandleFunc("/readings/aggregate", api.HandleAggregate)
	mux.HandleFunc("/sensors",        api.HandleSensors)
	mux.Handle("/health",             api.HandleHealth(broker))

	srv := &http.Server{
		Addr:         fmt.Sprintf(":%s", cfg.Port),
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 0, // SSE streams need no write timeout
		IdleTimeout:  120 * time.Second,
	}

	// ── Graceful shutdown ─────────────────────────────────────────────────────
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Printf("opcua-railway-go listening on :%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	<-quit
	log.Println("Shutting down...")
	cancel() // stop OPC UA goroutine

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutCancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		log.Printf("HTTP shutdown error: %v", err)
	}
	log.Println("Stopped")
}
