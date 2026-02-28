package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"mergereward-backend/internal/api"
	"mergereward-backend/internal/db"
	"mergereward-backend/internal/store"
	"mergereward-backend/internal/ws"
)

func main() {
	addr := envOr("ADDR", ":8080")

	// ── Store ─────────────────────────────────────────────────────────────────
	// Use Postgres when DATABASE_URL is set; fall back to in-memory.
	var st store.Store
	if dsn := os.Getenv("DATABASE_URL"); dsn != "" {
		pg, err := db.Open(dsn)
		if err != nil {
			log.Fatalf("postgres connect: %v", err)
		}
		defer pg.Close()
		st = pg
		log.Printf("store: postgres (%s)", maskDSN(dsn))
	} else {
		st = store.NewMemory()
		log.Printf("store: in-memory (set DATABASE_URL to use postgres)")
	}

	// ── Session secret ────────────────────────────────────────────────────────
	secret := os.Getenv("SESSION_SECRET")
	if secret == "" {
		log.Printf("warning: SESSION_SECRET not set; GitHub OAuth sessions will not work")
	}

	// ── WebSocket hub ─────────────────────────────────────────────────────────
	hub := ws.NewHub()
	go hub.Run()

	// ── HTTP server ───────────────────────────────────────────────────────────
	h := api.NewHandler(api.Deps{
		Store:         st,
		Hub:           hub,
		SessionSecret: secret,
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           h.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// maskDSN hides the password in a DSN for logging.
func maskDSN(dsn string) string {
	if len(dsn) > 30 {
		return dsn[:30] + "..."
	}
	return dsn
}
