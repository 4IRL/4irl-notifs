package database

import (
	"context"
	"testing"
)

func TestNewPoolSetsExplicitMaxConns(testInstance *testing.T) {
	pool, err := NewPool(context.Background(), Config{
		Host: "localhost", Port: "5432", User: "delivery", Name: "delivery", Password: "unused",
	})
	if err != nil {
		testInstance.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()
	if got := pool.Config().MaxConns; got != 8 {
		testInstance.Errorf("MaxConns = %d, want 8", got)
	}
}
