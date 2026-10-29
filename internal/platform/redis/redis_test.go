package redis

import (
	"context"
	"testing"
	"time"
)

// An optional caller keeps the client after a failed startup ping so a later
// command can reconnect; closing it here would make recovery impossible.
func TestNewReturnsClientWhenInitialPingFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	client, err := New(ctx, "127.0.0.1:1", DialTimeout(50*time.Millisecond))
	if err == nil {
		t.Fatal("want a ping error for an unreachable address")
	}
	if client == nil {
		t.Fatal("want the client alongside the error for lazy reconnect")
	}
	t.Cleanup(func() { _ = client.Close() })
}
