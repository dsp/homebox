package main

import (
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const serverVersion = "0.1.0"

// newMCPServer builds the MCP server with the HomeBox tool set. The tool
// surface is deliberately small and voice-friendly: tools accept names, do
// fuzzy resolution server-side, and report ambiguity back as a question the
// client can relay to the user.
func newMCPServer(hb *homeboxClient, readonly bool) *mcp.Server {
	s := &toolServer{hb: hb}

	server := mcp.NewServer(&mcp.Implementation{
		Name:    "homebox",
		Title:   "HomeBox Inventory",
		Version: serverVersion,
	}, nil)

	mcp.AddTool(server, &mcp.Tool{
		Name: "search_items",
		Description: "Search the inventory by free text. " +
			"Example: query='drill' finds every drill and drill accessory.",
	}, s.searchItems)

	mcp.AddTool(server, &mcp.Tool{
		Name: "get_item",
		Description: "Get one item's details including its full location path. " +
			"Example: item='DeWalt drill'.",
	}, s.getItem)

	mcp.AddTool(server, &mcp.Tool{
		Name: "where_is",
		Description: "Answer 'where is X?' — returns the item's full location path. " +
			"Example: item='passport'.",
	}, s.whereIs)

	mcp.AddTool(server, &mcp.Tool{
		Name: "list_locations",
		Description: "List every storage location with its full path. " +
			"Useful to disambiguate before creating or moving items.",
	}, s.listLocations)

	if !readonly {
		mcp.AddTool(server, &mcp.Tool{
			Name: "create_item",
			Description: "Add a new item to the inventory. " +
				"Example: name='DeWalt drill', location='garage shelf', quantity=1. " +
				"If the location is ambiguous the error lists candidates — ask the user which one.",
		}, s.createItem)

		mcp.AddTool(server, &mcp.Tool{
			Name: "set_quantity",
			Description: "Set an item's quantity to an absolute number. " +
				"Example: item='AA batteries', quantity=5 after the user says 'there are five left'.",
		}, s.setQuantity)

		mcp.AddTool(server, &mcp.Tool{
			Name: "move_item",
			Description: "Move an item to a different location. " +
				"Example: item='soldering iron', location='office drawer'.",
		}, s.moveItem)
	}

	return server
}

// newHandler wraps the streamable HTTP handler with bearer-token extraction.
// The server runs the 2026-07-28 stateless profile: no Mcp-Session-Id, every
// POST is self-contained, and the caller's HomeBox API key rides along in the
// request context for exactly one request.
func newHandler(server *mcp.Server, fallbackAPIKey string) http.Handler {
	streamable := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{
			Stateless:    true,
			JSONResponse: true,
		},
	)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := bearerToken(r)
		if key == "" {
			key = fallbackAPIKey
		}
		if key == "" {
			w.Header().Set("WWW-Authenticate", `Bearer realm="homebox-mcp"`)
			http.Error(w, "missing Authorization: Bearer <HomeBox API key>", http.StatusUnauthorized)
			return
		}

		streamable.ServeHTTP(w, r.WithContext(withAPIKey(r.Context(), key)))
	})
}

func bearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(auth) > len(prefix) && strings.EqualFold(auth[:len(prefix)], prefix) {
		return strings.TrimSpace(auth[len(prefix):])
	}
	return ""
}
