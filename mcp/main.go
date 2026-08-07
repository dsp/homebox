// Command homebox-mcp is a standalone MCP (Model Context Protocol) server
// exposing a HomeBox instance to MCP clients — voice assistants, chat
// agents, and automation tools.
//
// It is a thin, stateless proxy over the HomeBox REST API: every MCP request
// must carry a HomeBox API key (created under Profile → API Keys) as
// 'Authorization: Bearer hbox_...', and acts as the user owning that key.
//
// Configuration (environment variables):
//
//	HOMEBOX_URL         required, e.g. http://localhost:7745
//	HOMEBOX_MCP_LISTEN  listen address, default :7746
//	HOMEBOX_API_KEY     optional fallback key for clients that cannot send
//	                    custom headers; leave unset in multi-user setups
//	HOMEBOX_MCP_READONLY  set to "true" to register only query tools
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func main() {
	baseURL := os.Getenv("HOMEBOX_URL")
	if baseURL == "" {
		log.Fatal("HOMEBOX_URL is required, e.g. http://localhost:7745")
	}

	listen := os.Getenv("HOMEBOX_MCP_LISTEN")
	if listen == "" {
		listen = ":7746"
	}

	readonly, _ := strconv.ParseBool(os.Getenv("HOMEBOX_MCP_READONLY"))

	hb, err := newHomeboxClient(baseURL)
	if err != nil {
		log.Fatal(err)
	}

	handler := newHandler(newMCPServer(hb, readonly), os.Getenv("HOMEBOX_API_KEY"))

	srv := &http.Server{
		Addr:              listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("homebox-mcp %s listening on %s (homebox: %s, readonly: %v)", serverVersion, listen, baseURL, readonly)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}
