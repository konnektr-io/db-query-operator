package util

import (
	"context"
	"sync"
)

// MockDatabaseClient implements util.DatabaseClient for testing
// It returns configurable results and errors

type MockDatabaseClient struct {
	Rows      []RowResult
	Columns   []string
	ExecCalls []string
	FailQuery bool
	Mu        sync.RWMutex
}

func (m *MockDatabaseClient) QueryRead(ctx context.Context, query string) ([]RowResult, []string, error) {
	return m.Query(ctx, query)
}

func (m *MockDatabaseClient) QueryWrite(ctx context.Context, query string) ([]RowResult, []string, error) {
	return m.Query(ctx, query)
}

func (m *MockDatabaseClient) Connect(ctx context.Context, config map[string]string) error { return nil }
func (m *MockDatabaseClient) Query(ctx context.Context, query string) ([]RowResult, []string, error) {
	m.Mu.RLock()
	defer m.Mu.RUnlock()
	if m.FailQuery {
		return nil, nil, context.DeadlineExceeded
	}
	rowsCopy := make([]RowResult, len(m.Rows))
	copy(rowsCopy, m.Rows)
	columnsCopy := make([]string, len(m.Columns))
	copy(columnsCopy, m.Columns)
	return rowsCopy, columnsCopy, nil
}
func (m *MockDatabaseClient) Exec(ctx context.Context, query string) error {
	m.Mu.Lock()
	m.ExecCalls = append(m.ExecCalls, query)
	m.Mu.Unlock()
	return nil
}
func (m *MockDatabaseClient) Close(ctx context.Context) error { return nil }
