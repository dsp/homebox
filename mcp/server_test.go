package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const testAPIKey = "hbox_test_key"

// mockHomebox fakes the slice of the HomeBox REST API the tools use.
// Every handler asserts the caller's API key was forwarded.
func mockHomebox(t *testing.T) (*httptest.Server, *[]map[string]any) {
	t.Helper()

	var createdBodies []map[string]any

	shelfA := "11111111-1111-1111-1111-111111111111"
	shelfB := "22222222-2222-2222-2222-222222222222"
	garage := "33333333-3333-3333-3333-333333333333"
	drill := "44444444-4444-4444-4444-444444444444"

	mux := http.NewServeMux()

	requireAuth := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "Bearer "+testAPIKey {
			w.WriteHeader(http.StatusUnauthorized)
			return false
		}
		return true
	}

	mux.HandleFunc("GET /api/v1/entities/tree", func(w http.ResponseWriter, r *http.Request) {
		if !requireAuth(w, r) {
			return
		}
		_, _ = fmt.Fprintf(w, `[
			{"id":%q,"name":"Garage","type":"location","children":[
				{"id":%q,"name":"Shelf A","type":"location","children":[]},
				{"id":%q,"name":"Shelf B","type":"location","children":[]}
			]},
			{"id":"55555555-5555-5555-5555-555555555555","name":"Basement","type":"location","children":[]}
		]`, garage, shelfA, shelfB)
	})

	mux.HandleFunc("GET /api/v1/entities", func(w http.ResponseWriter, r *http.Request) {
		if !requireAuth(w, r) {
			return
		}
		if q := r.URL.Query().Get("q"); strings.Contains(strings.ToLower(q), "drill") {
			_, _ = fmt.Fprintf(w, `{"total":1,"items":[{"id":%q,"name":"DeWalt drill","quantity":1,"parent":{"id":%q,"name":"Shelf B"}}]}`, drill, shelfB)
			return
		}
		_, _ = fmt.Fprint(w, `{"total":0,"items":[]}`)
	})

	mux.HandleFunc("GET /api/v1/entities/"+drill+"/path", func(w http.ResponseWriter, r *http.Request) {
		if !requireAuth(w, r) {
			return
		}
		_, _ = fmt.Fprintf(w, `[
			{"id":%q,"type":"location","name":"Garage"},
			{"id":%q,"type":"location","name":"Shelf B"},
			{"id":%q,"type":"item","name":"DeWalt drill"}
		]`, garage, shelfB, drill)
	})

	mux.HandleFunc("POST /api/v1/entities", func(w http.ResponseWriter, r *http.Request) {
		if !requireAuth(w, r) {
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		createdBodies = append(createdBodies, body)
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"id":"66666666-6666-6666-6666-666666666666","name":%q,"quantity":%v}`,
			body["name"], body["quantity"])
	})

	mux.HandleFunc("PATCH /api/v1/entities/"+drill, func(w http.ResponseWriter, r *http.Request) {
		if !requireAuth(w, r) {
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = fmt.Fprintf(w, `{"id":%q,"name":"DeWalt drill","quantity":%v}`, drill, body["quantity"])
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &createdBodies
}

type authTransport struct {
	token string
}

func (a *authTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if a.token != "" {
		r.Header.Set("Authorization", "Bearer "+a.token)
	}
	return http.DefaultTransport.RoundTrip(r)
}

func connect(t *testing.T, endpoint, token string) *mcp.ClientSession {
	t.Helper()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:             endpoint,
		HTTPClient:           &http.Client{Transport: &authTransport{token: token}},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect failed: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func startMCP(t *testing.T, readonly bool) (endpoint string, created *[]map[string]any) {
	t.Helper()

	hbSrv, createdBodies := mockHomebox(t)
	hb, err := newHomeboxClient(hbSrv.URL)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(newHandler(newMCPServer(hb, readonly), ""))
	t.Cleanup(srv.Close)
	return srv.URL, createdBodies
}

func callTool(t *testing.T, session *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()

	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s) transport error: %v", name, err)
	}
	return result
}

func resultText(result *mcp.CallToolResult) string {
	var parts []string
	for _, c := range result.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func TestListToolsAndReadonly(t *testing.T) {
	endpoint, _ := startMCP(t, false)
	session := connect(t, endpoint, testAPIKey)

	// The server must negotiate the stateless 2026-07-28 revision (or later)
	// of the spec, not fall back to a session-based one.
	if v := session.InitializeResult().ProtocolVersion; v < "2026-07-28" {
		t.Errorf("expected protocol version >= 2026-07-28, got %q", v)
	}

	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 7 {
		t.Errorf("expected 7 tools, got %d", len(tools.Tools))
	}

	roEndpoint, _ := startMCP(t, true)
	roSession := connect(t, roEndpoint, testAPIKey)

	roTools, err := roSession.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(roTools.Tools) != 4 {
		t.Errorf("expected 4 readonly tools, got %d", len(roTools.Tools))
	}
	for _, tool := range roTools.Tools {
		switch tool.Name {
		case "create_item", "set_quantity", "move_item":
			t.Errorf("write tool %q registered in readonly mode", tool.Name)
		}
	}
}

func TestWhereIsResolvesPath(t *testing.T) {
	endpoint, _ := startMCP(t, false)
	session := connect(t, endpoint, testAPIKey)

	result := callTool(t, session, "where_is", map[string]any{"item": "dewalt drill"})
	if result.IsError {
		t.Fatalf("unexpected tool error: %s", resultText(result))
	}
	if text := resultText(result); !strings.Contains(text, "Garage › Shelf B") {
		t.Errorf("expected full path in response, got %q", text)
	}
}

func TestCreateItemResolvesLocation(t *testing.T) {
	endpoint, created := startMCP(t, false)
	session := connect(t, endpoint, testAPIKey)

	result := callTool(t, session, "create_item", map[string]any{
		"name":     "Impact driver",
		"location": "shelf b",
		"quantity": 2,
	})
	if result.IsError {
		t.Fatalf("unexpected tool error: %s", resultText(result))
	}
	if text := resultText(result); !strings.Contains(text, "Garage › Shelf B") {
		t.Errorf("expected confirmation with path, got %q", text)
	}

	if len(*created) != 1 {
		t.Fatalf("expected one create call, got %d", len(*created))
	}
	body := (*created)[0]
	if body["parentId"] != "22222222-2222-2222-2222-222222222222" {
		t.Errorf("expected parentId of Shelf B, got %v", body["parentId"])
	}
	if body["quantity"] != float64(2) {
		t.Errorf("expected quantity 2, got %v", body["quantity"])
	}
}

func TestCreateItemAmbiguousLocation(t *testing.T) {
	endpoint, created := startMCP(t, false)
	session := connect(t, endpoint, testAPIKey)

	result := callTool(t, session, "create_item", map[string]any{
		"name":     "Impact driver",
		"location": "shelf",
	})
	if !result.IsError {
		t.Fatal("expected ambiguity error for location 'shelf'")
	}
	text := resultText(result)
	if !strings.Contains(text, "Shelf A") || !strings.Contains(text, "Shelf B") {
		t.Errorf("expected candidates in error, got %q", text)
	}
	if len(*created) != 0 {
		t.Errorf("no create call should happen on ambiguity, got %d", len(*created))
	}
}

func TestSetQuantity(t *testing.T) {
	endpoint, _ := startMCP(t, false)
	session := connect(t, endpoint, testAPIKey)

	result := callTool(t, session, "set_quantity", map[string]any{"item": "DeWalt drill", "quantity": 5})
	if result.IsError {
		t.Fatalf("unexpected tool error: %s", resultText(result))
	}
	if text := resultText(result); !strings.Contains(text, "5") {
		t.Errorf("expected new quantity in response, got %q", text)
	}
}

func TestMissingAuthRejected(t *testing.T) {
	endpoint, _ := startMCP(t, false)

	resp, err := http.Post(endpoint, "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 without Authorization header, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
		t.Errorf("expected WWW-Authenticate challenge, got %q", got)
	}
}

func TestFlattenLocations(t *testing.T) {
	roots := []*treeItem{
		{ID: "1", Name: "Garage", Type: "location", Children: []*treeItem{
			{ID: "2", Name: "Shelf", Type: "location"},
			{ID: "3", Name: "Drill", Type: "item"}, // items in the tree are skipped
		}},
	}

	flat := flattenLocations(roots)
	if len(flat) != 2 {
		t.Fatalf("expected 2 locations, got %d", len(flat))
	}
	if flat[1].Path != "Garage › Shelf" {
		t.Errorf("expected nested path, got %q", flat[1].Path)
	}
}
