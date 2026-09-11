package main

import (
	"context"
	"log"
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

func main() {
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

	if err := pool.Ping(connectionContext); err != nil {
		log.Fatalf("connect to database: %v", err)
	}

	publisher, err := queue.NewStream(os.Getenv("REDIS_URL"), queue.DefaultStream, queue.DefaultGroup, "")
	if err != nil {
		log.Fatalf("configure job notifications: %v", err)
	}
	defer func() {
		_ = publisher.Close()
	}()

	if err := publisher.Ping(connectionContext); err != nil {
		log.Fatalf("connect to redis: %v", err)
	}
	if err := publisher.EnsureGroup(connectionContext); err != nil {
		log.Fatalf("prepare job stream: %v", err)
	}

	address := ":8080"
	if port := os.Getenv("PORT"); port != "" {
		address = ":" + port
	}

	server := &http.Server{
		Addr:              address,
		Handler:           api.NewHandler(api.NewPostgresStore(pool), embeddingClient, answerClient, publisher, extractor),
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("Recall API listening on %s", address)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
