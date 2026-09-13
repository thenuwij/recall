package main

import (
	"context"
	"errors"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/thenujawijesuriya/recall/internal/api"
	"github.com/thenujawijesuriya/recall/internal/embedding"
	"github.com/thenujawijesuriya/recall/internal/generation"
	"github.com/thenujawijesuriya/recall/internal/queue"
)

func workerCount() int {
	value := os.Getenv("WORKER_CONCURRENCY")
	if value == "" {
		return 2
	}

	count, err := strconv.Atoi(value)
	if err != nil || count < 1 {
		slog.Warn("ignoring invalid WORKER_CONCURRENCY", "value", value)
		return 2
	}

	return count
}

func healthAddress() string {
	port := os.Getenv("WORKER_HEALTH_PORT")
	if port == "" {
		port = "8081"
	}

	return ":" + port
}

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

	if err := waitFor("postgres", pool.Ping); err != nil {
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

	if err := waitFor("redis", notifier.Ping); err != nil {
		log.Fatalf("connect to redis: %v", err)
	}
	if err := waitFor("redis stream group", notifier.EnsureGroup); err != nil {
		log.Fatalf("prepare job stream: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	health := &http.Server{
		Addr: healthAddress(),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}` + "\n"))
		}),
	}
	go func() {
		if err := health.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("worker health server", "error", err)
		}
	}()
	defer func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = health.Shutdown(shutdownContext)
	}()

	slog.Info("worker started", "health_address", health.Addr)
	worker := api.NewWorker(api.NewPostgresStore(pool), embeddingClient, generationClient, notifier)
	if err := worker.RunPool(ctx, workerCount()); err != nil && ctx.Err() == nil {
		log.Fatalf("ingestion worker: %v", err)
	}
	slog.Info("worker stopped")
}
