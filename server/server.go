// Package server implements a TCP server for memdb.
//
// Protocol (line-based, UTF-8):
//
//	Client sends:  one SQL statement per line (newline-terminated)
//	Server replies: result lines followed by a status line
//
//	Success:
//	  DATA\t<col1>\t<col2>\t...\n   (schema header, only for SELECT)
//	  ROW\t<v1>\t<v2>\t...\n        (one per result row, only for SELECT)
//	  OK\t<rows_affected>\n          (always last line)
//
//	Error:
//	  ERR\t<message>\n
//
// This is intentionally simple — no authentication, no TLS, no connection
// pooling. A real production server would use a proper wire protocol
// (PostgreSQL wire, MySQL protocol, or gRPC). This demonstrates the concept.
package server

import (
	"bufio"
	"fmt"
	"net"
	"strings"

	"github.com/pandeylakshya207-max/memdb/db"
)

// Server is a TCP server that accepts SQL connections.
type Server struct {
	db       *db.DB
	listener net.Listener
	addr     string
}

// New creates a new Server backed by the given database.
func New(database *db.DB, addr string) *Server {
	return &Server{db: database, addr: addr}
}

// Listen binds the TCP listener and returns without blocking.
func (s *Server) Listen() error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("server.Listen %s: %w", s.addr, err)
	}
	s.listener = ln
	return nil
}

// Addr returns the address the server is listening on.
// Useful when addr was ":0" (OS-assigned port).
func (s *Server) Addr() string {
	if s.listener == nil {
		return s.addr
	}
	return s.listener.Addr().String()
}

// Serve accepts connections in a blocking loop.
// Returns when the listener is closed.
func (s *Server) Serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return // listener closed
		}
		go s.handleConn(conn)
	}
}

// Close stops the server.
func (s *Server) Close() error {
	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
}

// handleConn processes one client connection: read SQL lines, execute, write results.
func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()
	scanner := bufio.NewScanner(conn)
	w := bufio.NewWriter(conn)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line == "quit" || line == "exit" {
			break
		}

		res, err := s.db.Exec(line)
		if err != nil {
			fmt.Fprintf(w, "ERR\t%s\n", strings.ReplaceAll(err.Error(), "\n", " "))
			w.Flush()
			continue
		}

		// For SELECT: emit schema header then rows.
		if len(res.Rows) > 0 || res.Schema.Width() > 0 {
			// Schema line.
			colNames := make([]string, res.Schema.Width())
			for i, col := range res.Schema.Columns {
				colNames[i] = col.Name
			}
			fmt.Fprintf(w, "DATA\t%s\n", strings.Join(colNames, "\t"))
			// Row lines.
			for _, row := range res.Rows {
				vals := make([]string, len(row))
				for i, v := range row {
					vals[i] = v.String()
				}
				fmt.Fprintf(w, "ROW\t%s\n", strings.Join(vals, "\t"))
			}
		}
		fmt.Fprintf(w, "OK\t%d\n", res.RowsAffected)
		w.Flush()
	}
}
