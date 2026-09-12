package main

import (
	"context"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/thenujawijesuriya/recall/internal/api"
	"github.com/thenujawijesuriya/recall/internal/embedding"
	"github.com/thenujawijesuriya/recall/internal/generation"
	"github.com/thenujawijesuriya/recall/internal/queue"
)

func configureLogging() {
	level := slog.LevelInfo
	if err := level.UnmarshalText([]byte(os.Getenv("LOG_LEVEL"))); err != nil {
		level = slog.LevelInfo
	}

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})))
}

func main() {
	configureLogging()

	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("DATABASE_URL must be set")
	}
	embeddingClient, err := embedding.NewOpenAIClient(os.Getenv("OPENAI_API_KEY"))
	if err != nil {
		log.Fatalf("configure embedding client: %v", err)
	}
	generationClient, err := generation.NewOpenAIClient(os.Getenv("OPENAI_API_KEY"))
	if err != nil {
		log.Fatalf("configure generation client: %v", err)
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
	notifier, err := queue.NewStream(os.Getenv("REDIS_URL"), queue.DefaultStream, queue.DefaultGroup, "")
	if err != nil {
		log.Fatalf("configure job notifications: %v", err)
	}
	defer func() {
		_ = notifier.Close()
	}()

	if err := notifier.Ping(connectionContext); err != nil {
		log.Fatalf("connect to redis: %v", err)
	}
	if err := notifier.EnsureGroup(connectionContext); err != nil {
		log.Fatalf("prepare job stream: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	slog.Info("worker started")
	if err := api.NewWorker(api.NewPostgresStore(pool), embeddingClient, generationClient, notifier).Run(ctx); err != nil && ctx.Err() == nil {
		log.Fatalf("ingestion worker: %v", err)
	}
	slog.Info("worker stopped")
}
