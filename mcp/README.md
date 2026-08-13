# HomeBox MCP Server

A standalone [Model Context Protocol](https://modelcontextprotocol.io) server
that exposes a HomeBox instance to MCP clients — voice assistants, chat
agents, and automation tools. Ask "where is the soldering iron?", say "add
two spare AA packs to the basement shelf", and let the client's model drive
the inventory through a small, voice-friendly tool set.

It is a thin **stateless** proxy over the HomeBox REST API (MCP streamable
HTTP transport, spec revision 2026-07-28). It stores nothing and holds no
credentials: every request authenticates with a **HomeBox API key** and acts
as the user who owns that key.

## Tools

| Tool | What it does |
|---|---|
| `search_items` | Free-text search over the inventory |
| `get_item` | One item's details incl. full location path |
| `where_is` | "Where is X?" → `Garage › Shelf B` |
| `list_locations` | All storage locations with full paths |
| `create_item` | Add an item; location resolved fuzzily by name |
| `set_quantity` | Set an item's quantity |
| `move_item` | Move an item to another location |

Tools accept **names, not ids** (ids work too). Fuzzy matches are resolved
server-side; genuine ambiguity ("shelf" matches *Shelf A* and *Shelf B*)
comes back as an error listing the candidates so the client can ask the user
which one they meant. Nothing is created or changed on an ambiguous request.

## Running

```bash
cd mcp
HOMEBOX_URL=http://localhost:7745 go run .
# listening on :7746
```

| Variable | Default | Purpose |
|---|---|---|
| `HOMEBOX_URL` | *(required)* | Base URL of the HomeBox instance |
| `HOMEBOX_MCP_LISTEN` | `:7746` | Listen address |
| `HOMEBOX_MCP_READONLY` | `false` | Register only the query tools |
| `HOMEBOX_API_KEY` | *(unset)* | Fallback key for clients that cannot send headers — single-user setups only |

## Authentication

Create an API key in HomeBox under **Profile → API Keys**, then send it on
every MCP request:

```
Authorization: Bearer hbox_...
```

Requests without a key get `401`. Setting `HOMEBOX_API_KEY` as a server-wide
fallback **disables MCP-side authentication**: any request that reaches the
port gets full read/write access as that key's user. Use it only for
single-user setups where `:7746` is bound to localhost or a trusted network,
and never in multi-user setups.

### Client examples

Claude Code:

```bash
claude mcp add --transport http homebox http://localhost:7746/ \
  --header "Authorization: Bearer hbox_..."
```

Generic JSON config (clients that support remote HTTP servers with headers):

```json
{
  "mcpServers": {
    "homebox": {
      "type": "http",
      "url": "http://localhost:7746/",
      "headers": { "Authorization": "Bearer hbox_..." }
    }
  }
}
```

Clients that only speak stdio can bridge with
[`mcp-remote`](https://www.npmjs.com/package/mcp-remote):

```json
{
  "mcpServers": {
    "homebox": {
      "command": "npx",
      "args": [
        "-y", "mcp-remote", "http://localhost:7746/",
        "--header", "Authorization: Bearer hbox_..."
      ]
    }
  }
}
```

Smoke test with curl:

```bash
curl -s http://localhost:7746/ \
  -H "Authorization: Bearer hbox_..." \
  -H "Content-Type: application/json" \
  -H "Accept: application/json, text/event-stream" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2026-07-28","capabilities":{},"clientInfo":{"name":"curl","version":"0"}}}'
```

(The `Accept` header must list both types — the streamable transport rejects
requests that only accept JSON.)

## Notes

- **Stateless by design**: no `Mcp-Session-Id`, every POST self-contained;
  authenticated GET/DELETE return 405 (unauthenticated requests get 401
  first). This matches the sessionless direction of the MCP spec (SEP-2567)
  and makes the server safe to run behind any load balancer.
- The server needs network reach to `HOMEBOX_URL` only. Run it next to
  HomeBox (same compose file/network) and expose `:7746` however you expose
  HomeBox itself. Use HTTPS via your reverse proxy if the key crosses
  untrusted networks.
- OAuth for clients that require it (e.g. some hosted connector UIs) is out
  of scope here; front the server with an OAuth-terminating proxy if needed.

## Development

```bash
cd mcp && go test ./...
```

The module is self-contained (`github.com/sysadminsmedia/homebox/mcp`) and
does not import backend code — it talks to HomeBox exclusively through the
public REST API, so it can also be pointed at any existing instance.
