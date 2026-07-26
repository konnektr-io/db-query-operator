package util

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// redactPassword removes the password from a PostgreSQL connection string for safe logging.
// Input: postgres://user:password@host:port/dbname?sslmode=...
// Output: postgres://user:***@host:port/dbname?sslmode=...
func redactPassword(connString string) string {
	// Find the @ symbol that separates credentials from host
	atIdx := strings.LastIndex(connString, "@")
	if atIdx < 0 {
		return connString
	}
	// Find the colon before the @ that separates username from password
	colonIdx := strings.LastIndex(connString[:atIdx], ":")
	if colonIdx < 0 {
		return connString
	}
	schemeEnd := strings.Index(connString, "://")
	if schemeEnd < 0 || colonIdx < schemeEnd+3 {
		return connString
	}
	return connString[:colonIdx+1] + "***" + connString[atIdx:]
}

type PostgresDatabaseClient struct {
	connWrite *pgx.Conn // for writes
	connRead  *pgx.Conn // for reads (SELECT)
}

func (p *PostgresDatabaseClient) QueryRead(ctx context.Context, query string) ([]RowResult, []string, error) {
	statements := splitStatements(query)
	if len(statements) == 1 {
		return p.executeSingleQuery(ctx, p.connRead, statements[0])
	}
	if len(statements) > 1 {
		batch := &pgx.Batch{}
		for i := 0; i < len(statements)-1; i++ {
			batch.Queue(statements[i])
		}
		batchResults := p.connRead.SendBatch(ctx, batch)
		err := batchResults.Close()
		if err != nil {
			return nil, nil, fmt.Errorf("failed to execute batch commands: %w", err)
		}
		return p.executeSingleQuery(ctx, p.connRead, statements[len(statements)-1])
	}
	return nil, nil, fmt.Errorf("no statements to execute")
}

func (p *PostgresDatabaseClient) QueryWrite(ctx context.Context, query string) ([]RowResult, []string, error) {
	statements := splitStatements(query)
	if len(statements) == 1 {
		return p.executeSingleQuery(ctx, p.connWrite, statements[0])
	}
	if len(statements) > 1 {
		batch := &pgx.Batch{}
		for i := 0; i < len(statements)-1; i++ {
			batch.Queue(statements[i])
		}
		batchResults := p.connWrite.SendBatch(ctx, batch)
		err := batchResults.Close()
		if err != nil {
			return nil, nil, fmt.Errorf("failed to execute batch commands: %w", err)
		}
		return p.executeSingleQuery(ctx, p.connWrite, statements[len(statements)-1])
	}
	return nil, nil, fmt.Errorf("no statements to execute")
}

func (p *PostgresDatabaseClient) Connect(ctx context.Context, config map[string]string) error {
	// Connect to write (primary)
	connStringWrite := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=%s",
		config["username"], config["password"], config["host"], config["port"], config["dbname"], config["sslmode"])
	connWrite, err := pgx.Connect(ctx, connStringWrite)
	if err != nil {
		return fmt.Errorf("failed to connect to database: %w (conn: %s)",
			err, redactPassword(connStringWrite))
	}
	p.connWrite = connWrite

	// Connect to read (replica) if provided
	if readonlyHost, ok := config["readonly_host"]; ok && readonlyHost != "" {
		connStringRead := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=%s",
			config["username"], config["password"], readonlyHost, config["port"], config["dbname"], config["sslmode"])
		connRead, err := pgx.Connect(ctx, connStringRead)
		if err != nil {
			p.connRead = p.connWrite
		} else {
			p.connRead = connRead
		}
	} else {
		p.connRead = p.connWrite
	}
	return nil
}

// executeSingleQuery executes a single query statement and returns results
func (p *PostgresDatabaseClient) executeSingleQuery(ctx context.Context, conn *pgx.Conn, query string) ([]RowResult, []string, error) {
	rows, err := conn.Query(ctx, query)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	columnNames := make([]string, len(rows.FieldDescriptions()))
	for i, fd := range rows.FieldDescriptions() {
		columnNames[i] = string(fd.Name)
	}

	var results []RowResult
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return nil, nil, err
		}
		row := make(RowResult)
		for i, colName := range columnNames {
			row[colName] = values[i]
		}
		results = append(results, row)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	return results, columnNames, nil
}

// splitStatements splits a SQL query string into individual statements
// This is a simple implementation that splits on semicolons not inside quotes
func splitStatements(query string) []string {
	var statements []string
	var current strings.Builder
	inSingleQuote := false
	inDoubleQuote := false
	inDollarQuote := false
	dollarTag := ""

	runes := []rune(query)
	for i := 0; i < len(runes); i++ {
		ch := runes[i]

		// Handle dollar-quoted strings (e.g., $$...$$ or $tag$...$tag$)
		if ch == '$' && !inSingleQuote && !inDoubleQuote {
			// Check if this starts a dollar quote
			tagEnd := i + 1
			for tagEnd < len(runes) && (runes[tagEnd] != '$') {
				tagEnd++
			}
			if tagEnd < len(runes) {
				currentTag := string(runes[i : tagEnd+1])
				if !inDollarQuote {
					// Starting a dollar quote
					inDollarQuote = true
					dollarTag = currentTag
					current.WriteString(currentTag)
					i = tagEnd
					continue
				} else if currentTag == dollarTag {
					// Ending a dollar quote
					inDollarQuote = false
					current.WriteString(currentTag)
					dollarTag = ""
					i = tagEnd
					continue
				}
			}
		}

		// Handle single quotes
		if ch == '\'' && !inDoubleQuote && !inDollarQuote {
			inSingleQuote = !inSingleQuote
			current.WriteRune(ch)
			continue
		}

		// Handle double quotes
		if ch == '"' && !inSingleQuote && !inDollarQuote {
			inDoubleQuote = !inDoubleQuote
			current.WriteRune(ch)
			continue
		}

		// Handle semicolon (statement separator)
		if ch == ';' && !inSingleQuote && !inDoubleQuote && !inDollarQuote {
			stmt := strings.TrimSpace(current.String())
			if stmt != "" {
				statements = append(statements, stmt)
			}
			current.Reset()
			continue
		}

		current.WriteRune(ch)
	}

	// Add the last statement if there's anything left
	stmt := strings.TrimSpace(current.String())
	if stmt != "" {
		statements = append(statements, stmt)
	}

	return statements
}

func (p *PostgresDatabaseClient) Exec(ctx context.Context, query string) error {
	if p.connWrite == nil {
		return fmt.Errorf("no database connection")
	}
	_, err := p.connWrite.Exec(ctx, query)
	return err
}

func (p *PostgresDatabaseClient) Close(ctx context.Context) error {
	var err error
	if p.connRead != nil && p.connRead != p.connWrite {
		if e := p.connRead.Close(ctx); e != nil {
			err = e
		}
	}
	if p.connWrite != nil {
		if e := p.connWrite.Close(ctx); e != nil {
			err = e
		}
	}
	return err
}
