package main

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "Usage: %s <database-url> <migration-file>\n", os.Args[0])
		os.Exit(1)
	}

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		os.Exit(1)
	}
	defer conn.Close(ctx)

	sql, err := os.ReadFile(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, "read:", err)
		os.Exit(1)
	}

	_, err = conn.Exec(ctx, string(sql))
	if err != nil {
		fmt.Fprintln(os.Stderr, "exec:", err)
		os.Exit(1)
	}

	fmt.Printf("Migration %s applied successfully\n", os.Args[2])
}
