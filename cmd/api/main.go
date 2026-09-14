package main

import (
	"context"
	"log"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/thenujawijesuriya/recall/internal/api"
	"github.com/thenujawijesuriya/recall/internal/embedding"
	"github.com/thenujawijesuriya/recall/internal/extraction"
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

func waitFor(dependency string, check func(context.Context) error) error {
	deadline := time.Now().Add(time.Minute)
	for {
		attemptContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := check(attemptContext)
		cancel()
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}

		slog.Warn("waiting for dependency", "dependency", dependency, "error", err)
		time.Sleep(2 * time.Second)
	}
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
	answerClient, err := generation.NewOpenAIClient(os.Getenv("OPENAI_API_KEY"))
	if err != nil {
		log.Fatalf("configure answer client: %v", err)
	}

	extractor, err := extraction.NewPDFExtractor()
	if err != nil {
		log.Fatalf("configure pdf extraction: %v", err)
	}

	connectionContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(connectionContext, databaseURL)
	if err != nil {
		log.Fatalf("configure database connection: %v", err)
	}
	defer pool.Close()

	if err := waitFor("postgres", pool.Ping); err != nil {
		log.Fatalf("connect to database: %v", err)
	}

	publisher, err := queue.NewStream(os.Getenv("REDIS_URL"), queue.DefaultStream, queue.DefaultGroup, "")
	if err != nil {
		log.Fatalf("configure job notifications: %v", err)
	}
	defer func() {
		_ = publisher.Close()
	}()

	address := ":8080"
	if port := os.Getenv("PORT"); port != "" {
		address = ":" + port
	}

	server := &http.Server{
		Addr:              address,
		Handler:           api.NewHandler(api.NewPostgresStore(pool), embeddingClient, answerClient, publisher, extractor),
		ReadHeaderTimeout: 5 * time.Second,
	}

	slog.Info("api listening", "address", address)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
