package util

import (
	"context"
	"fmt"

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
