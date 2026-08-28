// Command memdb-server starts a memdb TCP server.
//
// Usage:
//
//	memdb-server [flags]
//
// Flags:
//
//	-dir   string   Database directory (default: ./memdb-data)
//	-addr  string   Listen address    (default: 0.0.0.0:5432)
//
// Protocol: send one SQL statement per line; receive DATA/ROW/OK/ERR lines.
//
// Quick demo (two terminals):
//
//	Terminal 1:  memdb-server -dir /tmp/mydb
//	Terminal 2:  nc localhost 5432
//	             CREATE TABLE users (id INT PRIMARY KEY, name TEXT)
//	             INSERT INTO users (id, name) VALUES (1, 'Alice')
//	             SELECT * FROM users
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/pandeylakshya207-max/memdb/db"
	"github.com/pandeylakshya207-max/memdb/server"
)

func main() {
	dir := flag.String("dir", "./memdb-data", "Database directory")
	addr := flag.String("addr", "0.0.0.0:5432", "Listen address (host:port)")
	flag.Parse()

	// Open (or create) the database.
	database, err := db.Open(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "memdb-server: open db: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	// Start server.
	srv := server.New(database, *addr)
	if err := srv.Listen(); err != nil {
		fmt.Fprintf(os.Stderr, "memdb-server: listen: %v\n", err)
		os.Exit(1)
	}

	log.Printf("memdb-server listening on %s (data dir: %s)", srv.Addr(), *dir)
	log.Printf("Connect with: nc %s", srv.Addr())
	log.Printf("Send SQL lines. Type 'quit' or Ctrl-C to exit.\n")

	// Handle Ctrl-C gracefully.
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Println("shutting down...")
		srv.Close()
	}()

	srv.Serve() // blocks until listener closed
	log.Println("memdb-server stopped")
}
