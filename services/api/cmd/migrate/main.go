// Command migrate applies Alpine's goose migrations (embedded from packages/shared/db).
//
//	migrate up | down | status | reset | redo | version | up-to N | down-to N
//
// Connects with DATABASE_ADMIN_URL (owner role; never the alpine_app role).
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/alpine-dev/alpine/packages/shared/db"
)

func main() {
	url := os.Getenv("DATABASE_ADMIN_URL")
	if url == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_ADMIN_URL is required (owner role connection string)")
		os.Exit(2)
	}
	cmd := "up"
	var args []string
	if len(os.Args) > 1 {
		cmd, args = os.Args[1], os.Args[2:]
	}
	if err := db.Migrate(context.Background(), url, cmd, args...); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}
