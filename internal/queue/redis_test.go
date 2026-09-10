package queue

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

func testStream(t *testing.T) *Stream {
	t.Helper()

	redisURL := os.Getenv("RECALL_TEST_REDIS_URL")
	if redisURL == "" {
		t.Skip("set RECALL_TEST_REDIS_URL to run Redis integration tests")
	}

	name := fmt.Sprintf("recall:test:%s:%d", t.Name(), time.Now().UnixNano())
	stream, err := NewStream(redisURL, name, DefaultGroup, "test-consumer")
	if err != nil {
		t.Fatalf("new stream: %v", err)
	}

	ctx := context.Background()
	if err := stream.Ping(ctx); err != nil {
		t.Fatalf("ping redis: %v", err)
	}
	if err := stream.EnsureGroup(ctx); err != nil {
		t.Fatalf("ensure group: %v", err)
	}

	t.Cleanup(func() {
		if err := stream.client.Del(ctx, name).Err(); err != nil {
			t.Errorf("clean up stream: %v", err)
		}
		if err := stream.Close(); err != nil {
			t.Errorf("close stream: %v", err)
		}
	})

	return stream
}

func TestStreamRequiresARedisURL(t *testing.T) {
	if _, err := NewStream("", "", "", ""); err != ErrInvalidRedisURL {
		t.Fatalf("NewStream(\"\") error = %v, want ErrInvalidRedisURL", err)
	}
}

func TestStreamRejectsAMalformedURL(t *testing.T) {
	if _, err := NewStream("not-a-url", "", "", ""); err == nil {
		t.Fatal("NewStream() error = nil, want a parse failure")
	}
}

func TestStreamPublishThenReceiveCarriesTheJobID(t *testing.T) {
	stream := testStream(t)
	ctx := context.Background()

	if err := stream.Publish(ctx, "job-42"); err != nil {
		t.Fatalf("publish: %v", err)
	}

	messages, err := stream.Receive(ctx, 10, time.Second)
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(messages))
	}
	if messages[0].JobID != "job-42" {
		t.Errorf("JobID = %q, want %q", messages[0].JobID, "job-42")
	}
}

func TestStreamReceiveReturnsNothingWhenIdle(t *testing.T) {
	stream := testStream(t)

	messages, err := stream.Receive(context.Background(), 10, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if len(messages) != 0 {
		t.Errorf("messages = %d, want 0 for an idle stream", len(messages))
	}
}

func TestStreamUnacknowledgedMessageStaysPending(t *testing.T) {
	stream := testStream(t)
	ctx := context.Background()

	if err := stream.Publish(ctx, "job-42"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if _, err := stream.Receive(ctx, 10, time.Second); err != nil {
		t.Fatalf("receive: %v", err)
	}

	pending, err := stream.Pending(ctx)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if pending != 1 {
		t.Errorf("pending = %d, want 1: reading a message must not remove it", pending)
	}
}

func TestStreamAcknowledgementClearsPending(t *testing.T) {
	stream := testStream(t)
	ctx := context.Background()

	if err := stream.Publish(ctx, "job-42"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	messages, err := stream.Receive(ctx, 10, time.Second)
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if err := stream.Ack(ctx, messages[0].ID); err != nil {
		t.Fatalf("ack: %v", err)
	}

	pending, err := stream.Pending(ctx)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if pending != 0 {
		t.Errorf("pending = %d, want 0 once acknowledged", pending)
	}
}

func TestStreamDeliversEachMessageToOneConsumerInAGroup(t *testing.T) {
	first := testStream(t)
	ctx := context.Background()

	second, err := NewStream(os.Getenv("RECALL_TEST_REDIS_URL"), first.stream, first.group, "second-consumer")
	if err != nil {
		t.Fatalf("new second consumer: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	if err := first.Publish(ctx, "job-42"); err != nil {
		t.Fatalf("publish: %v", err)
	}

	firstBatch, err := first.Receive(ctx, 10, time.Second)
	if err != nil {
		t.Fatalf("first receive: %v", err)
	}
	secondBatch, err := second.Receive(ctx, 10, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("second receive: %v", err)
	}

	if len(firstBatch)+len(secondBatch) != 1 {
		t.Errorf("total delivered = %d, want 1: a consumer group hands each message to one consumer", len(firstBatch)+len(secondBatch))
	}
}

func TestStreamEnsureGroupIsRepeatable(t *testing.T) {
	stream := testStream(t)

	if err := stream.EnsureGroup(context.Background()); err != nil {
		t.Fatalf("second EnsureGroup: %v", err)
	}
}
