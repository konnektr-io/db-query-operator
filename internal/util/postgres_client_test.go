package util

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRedactPassword(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple connection string",
			input:    "postgres://user:secret123@localhost:5432/mydb?sslmode=disable",
			expected: "postgres://user:***@localhost:5432/mydb?sslmode=disable",
		},
		{
			name:     "with special chars in password",
			input:    "postgres://admin:p@ssw0rd!@host:5432/db?sslmode=require",
			expected: "postgres://admin:***@host:5432/db?sslmode=require",
		},
		{
			name:     "no password",
			input:    "postgres://user@localhost:5432/db",
			expected: "postgres://user@localhost:5432/db",
		},
		{
			name:     "no credentials",
			input:    "postgres://localhost:5432/db",
			expected: "postgres://localhost:5432/db",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := redactPassword(tt.input)
			require.Equal(t, tt.expected, result)
		})
	}
}
