package util

import (
	"context"
)

// RowResult represents a single row from a database query.
type RowResult map[string]interface{}

// DatabaseClient abstracts database operations for the controller.
type DatabaseClient interface {
	Connect(ctx context.Context, config map[string]string) error
	Query(ctx context.Context, query string) ([]RowResult, []string, error)
	Exec(ctx context.Context, query string) error
	Close(ctx context.Context) error
}
