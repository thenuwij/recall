package queue

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	DefaultStream = "recall:ingestion"
	DefaultGroup  = "ingestion-workers"

	jobIDField = "job_id"
)

var ErrInvalidRedisURL = errors.New("redis URL must not be empty")

type Message struct {
	ID    string
	JobID string
}

type Stream struct {
	client   *redis.Client
	stream   string
	group    string
	consumer string
}

func NewStream(redisURL, stream, group, consumer string) (*Stream, error) {
	if redisURL == "" {
		return nil, ErrInvalidRedisURL
	}

	options, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis URL: %w", err)
	}

	if stream == "" {
		stream = DefaultStream
	}
	if group == "" {
		group = DefaultGroup
	}
	if consumer == "" {
		consumer = defaultConsumerName()
	}

	options.DialTimeout = time.Second
	options.ReadTimeout = 3 * time.Second
	options.WriteTimeout = time.Second
	options.MaxRetries = -1
	options.ContextTimeoutEnabled = true

	return &Stream{
		client:   redis.NewClient(options),
		stream:   stream,
		group:    group,
		consumer: consumer,
	}, nil
}

func defaultConsumerName() string {
	host, err := os.Hostname()
	if err != nil {
		host = "worker"
	}

	return host + "-" + strconv.Itoa(os.Getpid())
}

func (s *Stream) Ping(ctx context.Context) error {
	return s.client.Ping(ctx).Err()
}

func (s *Stream) EnsureGroup(ctx context.Context) error {
	err := s.client.XGroupCreateMkStream(ctx, s.stream, s.group, "0").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return fmt.Errorf("create consumer group: %w", err)
	}

	return nil
}

func (s *Stream) Publish(ctx context.Context, jobID string) error {
	return s.client.XAdd(ctx, &redis.XAddArgs{
		Stream: s.stream,
		MaxLen: 1000,
		Approx: true,
		Values: map[string]any{jobIDField: jobID},
	}).Err()
}

func (s *Stream) Receive(ctx context.Context, count int64, block time.Duration) ([]Message, error) {
	if err := s.EnsureGroup(ctx); err != nil {
		return nil, err
	}
	streams, err := s.client.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    s.group,
		Consumer: s.consumer,
		Streams:  []string{s.stream, ">"},
		Count:    count,
		Block:    block,
	}).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	messages := make([]Message, 0, count)
	for _, stream := range streams {
		for _, entry := range stream.Messages {
			jobID, _ := entry.Values[jobIDField].(string)
			messages = append(messages, Message{ID: entry.ID, JobID: jobID})
		}
	}

	return messages, nil
}

func (s *Stream) Ack(ctx context.Context, ids ...string) error {
	if len(ids) == 0 {
		return nil
	}

	return s.client.XAck(ctx, s.stream, s.group, ids...).Err()
}

func (s *Stream) Pending(ctx context.Context) (int64, error) {
	pending, err := s.client.XPending(ctx, s.stream, s.group).Result()
	if err != nil {
		return 0, err
	}

	return pending.Count, nil
}

func (s *Stream) Close() error {
	return s.client.Close()
}
