package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/thenujawijesuriya/recall/internal/api"
	"github.com/thenujawijesuriya/recall/internal/embedding"
)

func main() {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("DATABASE_URL must be set")
	}
	embeddingClient, err := embedding.NewOpenAIClient(os.Getenv("OPENAI_API_KEY"))
	if err != nil {
		log.Fatalf("configure embedding client: %v", err)
	}

	connectionContext, cancelConnection := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelConnection()

	pool, err := pgxpool.New(connectionContext, databaseURL)
	if err != nil {
		log.Fatalf("configure database connection: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(connectionContext); err != nil {
		log.Fatalf("connect to database: %v", err)
	}

	// A cancelled context stops the loop between jobs rather than mid-job, so
	// an interrupted worker leaves its lease to expire instead of losing work.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Print("Recall ingestion worker started")
	if err := api.NewWorker(api.NewPostgresStore(pool), embeddingClient).Run(ctx); err != nil && ctx.Err() == nil {
		log.Fatalf("ingestion worker: %v", err)
	}
	log.Print("Recall ingestion worker stopped")
}
