package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// pathSeparator joins location segments in human-readable paths, matching the
// breadcrumb style of the HomeBox UI.
const pathSeparator = " › "

// maxAmbiguityCandidates bounds how many alternatives an ambiguity error
// lists — enough for a voice assistant to ask "which one?", not a data dump.
const maxAmbiguityCandidates = 5

type toolServer struct {
	hb *homeboxClient
}

// --- location resolution -----------------------------------------------------

type flatLocation struct {
	ID   string
	Name string
	Path string
}

func flattenLocations(roots []*treeItem) []flatLocation {
	var out []flatLocation
	var walk func(node *treeItem, prefix string)
	walk = func(node *treeItem, prefix string) {
		if node == nil || node.Type != "location" {
			return
		}
		path := node.Name
		if prefix != "" {
			path = prefix + pathSeparator + node.Name
		}
		out = append(out, flatLocation{ID: node.ID, Name: node.Name, Path: path})
		for _, child := range node.Children {
			walk(child, path)
		}
	}
	for _, root := range roots {
		walk(root, "")
	}
	return out
}

// resolveLocation matches a spoken location name against the location tree.
// Exact name matches win; otherwise substring matches on the name or full
// path are accepted. More than one match is an error that lists the
// candidates so the client can ask the user to disambiguate.
func (s *toolServer) resolveLocation(ctx context.Context, name string) (flatLocation, error) {
	roots, err := s.hb.locationTree(ctx)
	if err != nil {
		return flatLocation{}, err
	}
	locations := flattenLocations(roots)

	needle := strings.ToLower(strings.TrimSpace(name))
	if needle == "" {
		return flatLocation{}, fmt.Errorf("location name is empty")
	}

	var exact, fuzzy []flatLocation
	for _, loc := range locations {
		switch {
		case strings.ToLower(loc.Name) == needle || strings.ToLower(loc.Path) == needle:
			exact = append(exact, loc)
		case strings.Contains(strings.ToLower(loc.Name), needle) || strings.Contains(strings.ToLower(loc.Path), needle):
			fuzzy = append(fuzzy, loc)
		}
	}

	candidates := exact
	if len(candidates) == 0 {
		candidates = fuzzy
	}

	switch len(candidates) {
	case 0:
		return flatLocation{}, fmt.Errorf("no location matches %q; use list_locations to see the available locations", name)
	case 1:
		return candidates[0], nil
	default:
		return flatLocation{}, fmt.Errorf("location %q is ambiguous, ask the user which one they mean: %s",
			name, joinCandidates(candidatePaths(candidates)))
	}
}

func candidatePaths(locations []flatLocation) []string {
	paths := make([]string, 0, len(locations))
	for _, loc := range locations {
		paths = append(paths, loc.Path)
	}
	return paths
}

func joinCandidates(names []string) string {
	if len(names) > maxAmbiguityCandidates {
		names = append(names[:maxAmbiguityCandidates:maxAmbiguityCandidates],
			fmt.Sprintf("… and %d more", len(names)-maxAmbiguityCandidates))
	}
	return strings.Join(names, "; ")
}

// --- item resolution ---------------------------------------------------------

// resolveItem accepts either an entity UUID or a (possibly partial) item
// name. Name lookups go through search; a unique exact-name match wins over
// fuzzy matches, and remaining ambiguity is surfaced with candidates.
func (s *toolServer) resolveItem(ctx context.Context, ref string) (entitySummary, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return entitySummary{}, fmt.Errorf("item is empty; pass an item name or id")
	}

	if _, err := uuid.Parse(ref); err == nil {
		return s.hb.getEntity(ctx, ref)
	}

	result, err := s.hb.searchEntities(ctx, ref, 25)
	if err != nil {
		return entitySummary{}, err
	}

	var exact []entitySummary
	for _, item := range result.Items {
		if strings.EqualFold(item.Name, ref) {
			exact = append(exact, item)
		}
	}

	candidates := exact
	if len(candidates) == 0 {
		candidates = result.Items
	}

	switch len(candidates) {
	case 0:
		return entitySummary{}, fmt.Errorf("no item matches %q", ref)
	case 1:
		return candidates[0], nil
	default:
		names := make([]string, 0, len(candidates))
		for _, item := range candidates {
			label := item.Name
			if item.Parent != nil {
				label += " (in " + item.Parent.Name + ")"
			}
			names = append(names, label)
		}
		return entitySummary{}, fmt.Errorf("item %q is ambiguous, ask the user which one they mean: %s",
			ref, joinCandidates(names))
	}
}

func (s *toolServer) pathString(ctx context.Context, id string) string {
	segments, err := s.hb.entityPath(ctx, id)
	if err != nil || len(segments) == 0 {
		return ""
	}
	names := make([]string, 0, len(segments))
	for _, seg := range segments {
		names = append(names, seg.Name)
	}
	return strings.Join(names, pathSeparator)
}

// --- tool inputs/outputs -----------------------------------------------------

type itemResult struct {
	ID          string  `json:"id" jsonschema:"HomeBox entity id"`
	Name        string  `json:"name"`
	Description string  `json:"description,omitempty"`
	Quantity    float64 `json:"quantity"`
	Location    string  `json:"location,omitempty" jsonschema:"Full location path of the item"`
}

func textResult(format string, args ...any) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(format, args...)}},
	}
}

type searchItemsInput struct {
	Query string `json:"query" jsonschema:"Free-text search over item names and descriptions"`
	Limit int    `json:"limit,omitempty" jsonschema:"Maximum results to return (default 10)"`
}

type searchItemsOutput struct {
	Items []itemResult `json:"items"`
	Total int          `json:"total" jsonschema:"Total matches in HomeBox, which may exceed len(items)"`
}

func (s *toolServer) searchItems(ctx context.Context, _ *mcp.CallToolRequest, in searchItemsInput) (*mcp.CallToolResult, searchItemsOutput, error) {
	limit := in.Limit
	if limit <= 0 || limit > 50 {
		limit = 10
	}

	result, err := s.hb.searchEntities(ctx, in.Query, limit)
	if err != nil {
		return nil, searchItemsOutput{}, err
	}

	out := searchItemsOutput{Total: result.Total, Items: make([]itemResult, 0, len(result.Items))}
	lines := make([]string, 0, len(result.Items))
	for _, item := range result.Items {
		mapped := itemResult{ID: item.ID, Name: item.Name, Quantity: item.Quantity}
		if item.Parent != nil {
			mapped.Location = item.Parent.Name
		}
		out.Items = append(out.Items, mapped)

		line := fmt.Sprintf("%s (qty %g)", item.Name, item.Quantity)
		if mapped.Location != "" {
			line += " — " + mapped.Location
		}
		lines = append(lines, line)
	}

	if len(lines) == 0 {
		return textResult("No items match %q.", in.Query), out, nil
	}
	return textResult("%d match(es), showing %d:\n%s", result.Total, len(lines), strings.Join(lines, "\n")), out, nil
}

type itemRefInput struct {
	Item string `json:"item" jsonschema:"Item name or id. Prefer names; ambiguity is reported back with candidates"`
}

func (s *toolServer) getItem(ctx context.Context, _ *mcp.CallToolRequest, in itemRefInput) (*mcp.CallToolResult, itemResult, error) {
	item, err := s.resolveItem(ctx, in.Item)
	if err != nil {
		return nil, itemResult{}, err
	}

	out := itemResult{
		ID:          item.ID,
		Name:        item.Name,
		Description: item.Description,
		Quantity:    item.Quantity,
		Location:    s.pathString(ctx, item.ID),
	}
	return textResult("%s — quantity %g, located in %s. %s", out.Name, out.Quantity, orUnknown(out.Location), out.Description), out, nil
}

func (s *toolServer) whereIs(ctx context.Context, _ *mcp.CallToolRequest, in itemRefInput) (*mcp.CallToolResult, itemResult, error) {
	item, err := s.resolveItem(ctx, in.Item)
	if err != nil {
		return nil, itemResult{}, err
	}

	out := itemResult{ID: item.ID, Name: item.Name, Quantity: item.Quantity, Location: s.pathString(ctx, item.ID)}
	if out.Location == "" {
		return textResult("%s has no recorded location.", out.Name), out, nil
	}
	return textResult("%s is in %s.", out.Name, out.Location), out, nil
}

type createItemInput struct {
	Name        string  `json:"name" jsonschema:"Name of the new item"`
	Location    string  `json:"location,omitempty" jsonschema:"Location name, matched fuzzily against the location tree"`
	Quantity    float64 `json:"quantity,omitempty" jsonschema:"How many, defaults to 1"`
	Description string  `json:"description,omitempty"`
}

func (s *toolServer) createItem(ctx context.Context, _ *mcp.CallToolRequest, in createItemInput) (*mcp.CallToolResult, itemResult, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, itemResult{}, fmt.Errorf("name is required")
	}

	req := entityCreateRequest{
		Name:        strings.TrimSpace(in.Name),
		Description: in.Description,
		Quantity:    in.Quantity,
	}
	if req.Quantity <= 0 {
		req.Quantity = 1
	}

	var locationPath string
	if strings.TrimSpace(in.Location) != "" {
		loc, err := s.resolveLocation(ctx, in.Location)
		if err != nil {
			return nil, itemResult{}, err
		}
		req.ParentID = loc.ID
		locationPath = loc.Path
	}

	created, err := s.hb.createEntity(ctx, req)
	if err != nil {
		return nil, itemResult{}, err
	}

	out := itemResult{
		ID:          created.ID,
		Name:        created.Name,
		Description: created.Description,
		Quantity:    created.Quantity,
		Location:    locationPath,
	}
	if locationPath == "" {
		return textResult("Created %q (quantity %g).", out.Name, out.Quantity), out, nil
	}
	return textResult("Created %q (quantity %g) in %s.", out.Name, out.Quantity, locationPath), out, nil
}

type setQuantityInput struct {
	Item     string  `json:"item" jsonschema:"Item name or id"`
	Quantity float64 `json:"quantity" jsonschema:"New absolute quantity"`
}

func (s *toolServer) setQuantity(ctx context.Context, _ *mcp.CallToolRequest, in setQuantityInput) (*mcp.CallToolResult, itemResult, error) {
	if in.Quantity < 0 {
		return nil, itemResult{}, fmt.Errorf("quantity must be zero or positive")
	}

	item, err := s.resolveItem(ctx, in.Item)
	if err != nil {
		return nil, itemResult{}, err
	}

	quantity := in.Quantity
	updated, err := s.hb.patchEntity(ctx, entityPatchRequest{ID: item.ID, Quantity: &quantity})
	if err != nil {
		return nil, itemResult{}, err
	}

	out := itemResult{ID: updated.ID, Name: updated.Name, Quantity: updated.Quantity}
	return textResult("%s now has quantity %g.", out.Name, out.Quantity), out, nil
}

type moveItemInput struct {
	Item     string `json:"item" jsonschema:"Item name or id"`
	Location string `json:"location" jsonschema:"Destination location name, matched fuzzily against the location tree"`
}

func (s *toolServer) moveItem(ctx context.Context, _ *mcp.CallToolRequest, in moveItemInput) (*mcp.CallToolResult, itemResult, error) {
	item, err := s.resolveItem(ctx, in.Item)
	if err != nil {
		return nil, itemResult{}, err
	}

	loc, err := s.resolveLocation(ctx, in.Location)
	if err != nil {
		return nil, itemResult{}, err
	}

	updated, err := s.hb.patchEntity(ctx, entityPatchRequest{ID: item.ID, ParentID: loc.ID})
	if err != nil {
		return nil, itemResult{}, err
	}

	out := itemResult{ID: updated.ID, Name: updated.Name, Quantity: updated.Quantity, Location: loc.Path}
	return textResult("Moved %s to %s.", out.Name, loc.Path), out, nil
}

type listLocationsOutput struct {
	Locations []locationResult `json:"locations"`
}

type locationResult struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path" jsonschema:"Full path from the root location"`
}

func (s *toolServer) listLocations(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, listLocationsOutput, error) {
	roots, err := s.hb.locationTree(ctx)
	if err != nil {
		return nil, listLocationsOutput{}, err
	}

	flat := flattenLocations(roots)
	out := listLocationsOutput{Locations: make([]locationResult, 0, len(flat))}
	paths := make([]string, 0, len(flat))
	for _, loc := range flat {
		out.Locations = append(out.Locations, locationResult(loc))
		paths = append(paths, loc.Path)
	}

	if len(paths) == 0 {
		return textResult("No locations exist yet."), out, nil
	}
	return textResult("Locations:\n%s", strings.Join(paths, "\n")), out, nil
}

func orUnknown(s string) string {
	if s == "" {
		return "an unknown location"
	}
	return s
}
