// See protocol.go's package doc for the full "NOT FIX-certified"
// warning. This file is the plain TCP accept loop: one goroutine per
// connection, one SessionState (and therefore one sequence-number space)
// per connection — a genuine institutional FIX session is exactly this
// shape (one persistent TCP session per counterparty relationship), even
// though everything else about this gateway is illustrative.
package dmagateway

import (
	"bufio"
	"log"
	"net"
)

// Server is the DMA/FIX-inspired TCP listener.
type Server struct {
	listenAddress string
	submitOrder   OrderSubmitFunc
}

// NewServer builds a Server that will submit every accepted
// NEW_ORDER_SINGLE through submitOrder — see OrderSubmitFunc's doc
// comment for why this is how the real risk-check/audit-trail/
// matching-engine pipeline gets reused instead of duplicated.
func NewServer(listenAddress string, submitOrder OrderSubmitFunc) *Server {
	return &Server{listenAddress: listenAddress, submitOrder: submitOrder}
}

// ListenAndServe blocks accepting connections until the listener itself
// fails to start (e.g. the port is already in use) — intended to be run
// in its own goroutine from cmd/server/main.go, exactly like
// http.ListenAndServe.
func (server *Server) ListenAndServe() error {
	listener, listenError := net.Listen("tcp", server.listenAddress)
	if listenError != nil {
		return listenError
	}
	defer listener.Close()

	log.Printf("DMA/FIX-inspired gateway (NOT FIX-certified — see internal/dmagateway package doc) listening on %s", server.listenAddress)

	for {
		connection, acceptError := listener.Accept()
		if acceptError != nil {
			log.Printf("dmagateway: accept error: %v", acceptError)
			continue
		}
		go server.handleConnection(connection)
	}
}

func (server *Server) handleConnection(connection net.Conn) {
	defer connection.Close()

	sessionState := NewSessionState()
	sessionHandler := NewSessionHandler(sessionState, server.submitOrder)

	scanner := bufio.NewScanner(connection)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		responseLines, shouldClose := sessionHandler.HandleLine(line)
		for _, responseLine := range responseLines {
			if _, writeError := connection.Write([]byte(responseLine)); writeError != nil {
				log.Printf("dmagateway: write error, closing connection: %v", writeError)
				return
			}
		}
		if shouldClose {
			return
		}
	}
}
