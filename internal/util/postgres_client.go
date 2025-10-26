package util

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

type PostgresDatabaseClient struct {
	conn *pgx.Conn
}

func (p *PostgresDatabaseClient) Connect(ctx context.Context, config map[string]string) error {
	connString := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=%s",
		config["username"], config["password"], config["host"], config["port"], config["dbname"], config["sslmode"])
	conn, err := pgx.Connect(ctx, connString)
	if err != nil {
		return err
	}
	p.conn = conn
	return nil
}

func (p *PostgresDatabaseClient) Query(ctx context.Context, query string) ([]RowResult, []string, error) {
	// Split query into multiple statements if separated by semicolons
	statements := splitStatements(query)
	
	// If there's only one statement, execute it directly
	if len(statements) == 1 {
		return p.executeSingleQuery(ctx, statements[0])
	}
	
	// Multiple statements: use batch for all but the last, then query the last one
	if len(statements) > 1 {
		batch := &pgx.Batch{}
		// Add all statements except the last to the batch
		for i := 0; i < len(statements)-1; i++ {
			batch.Queue(statements[i])
		}
		
		// Execute the batch (setup commands like LOAD, SET)
		batchResults := p.conn.SendBatch(ctx, batch)
		// Close the batch results to ensure all commands are executed
		err := batchResults.Close()
		if err != nil {
			return nil, nil, fmt.Errorf("failed to execute batch commands: %w", err)
		}
		
		// Execute the final statement (the actual query that returns data)
		return p.executeSingleQuery(ctx, statements[len(statements)-1])
	}
	
	return nil, nil, fmt.Errorf("no statements to execute")
}

// executeSingleQuery executes a single query statement and returns results
func (p *PostgresDatabaseClient) executeSingleQuery(ctx context.Context, query string) ([]RowResult, []string, error) {
	rows, err := p.conn.Query(ctx, query)
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
	if p.conn == nil {
		return fmt.Errorf("no database connection")
	}
	_, err := p.conn.Exec(ctx, query)
	return err
}

func (p *PostgresDatabaseClient) Close(ctx context.Context) error {
	if p.conn != nil {
		return p.conn.Close(ctx)
	}
	return nil
}
