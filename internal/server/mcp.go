package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/paytonmurray51/workdesk/internal/db"
)

const mcpInstructions = `Workdesk is the user's self-hosted dashboard of boards (e.g. "Work", "Personal"),
each with lists (a daily "Today" list plus others), a morning briefing, an inbox for triage,
widgets (calendar/Jira snapshots) and proposed timeboxes for the day.
Call list_boards first. Boards can be named by name or id. Always set source_id (for example
the Jira key, email message id or event id) when posting items, inbox entries or briefings,
so re-runs update instead of duplicating, and include source_url so people can jump back.
Never put passwords, tokens or full email bodies in anything you post.`

// mcpHandler serves MCP over Streamable HTTP. Each request builds a server bound to the
// caller's principal (board token or OAuth grant), so tools can only see allowed boards.
func (s *Server) mcpHandler() http.Handler {
	h := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		p, _ := r.Context().Value(principalKey{}).(*principal)
		if p == nil {
			return nil
		}
		return s.newMCPServer(p)
	}, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, MaxRequestBodyBytes: 1 << 20})

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		challenge := fmt.Sprintf(`Bearer resource_metadata=%q, scope=%q`, s.resourceMetadataURL(), oauthScope)
		auth := r.Header.Get("Authorization")
		if auth == "" {
			w.Header().Set("WWW-Authenticate", challenge)
			writeErr(w, http.StatusUnauthorized, "bearer token required")
			return
		}
		p, err := s.authenticate(r)
		if err != nil {
			w.Header().Set("WWW-Authenticate", challenge+`, error="invalid_token"`)
			writeErr(w, http.StatusUnauthorized, "invalid or expired token")
			return
		}
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
}

type principalKey struct{}

func (s *Server) newMCPServer(p *principal) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "workdesk", Title: "Workdesk", Version: "0.2.0"},
		&mcp.ServerOptions{Instructions: mcpInstructions, SchemaCache: s.schemas})
	t := &mcpTools{s: s, p: p}

	mcp.AddTool(srv, &mcp.Tool{Name: "list_boards", Description: "List the boards this connection can use, with ids, names and time zones."}, t.listBoards)
	mcp.AddTool(srv, &mcp.Tool{Name: "list_lists", Description: "List a board's lists (id, name, kind daily|standing)."}, t.listLists)
	mcp.AddTool(srv, &mcp.Tool{Name: "create_list", Description: "Create a list on a board. If a list with that name exists it is returned unchanged."}, t.createList)
	mcp.AddTool(srv, &mcp.Tool{Name: "list_open_items", Description: "List unchecked items on a list. Name the list by list_id, or by board + list name (default \"Today\")."}, t.listOpenItems)
	mcp.AddTool(srv, &mcp.Tool{Name: "add_items", Description: "Add to-do items to a list (default the board's \"Today\" list). Items with a source_id are upserted: re-posting updates title/link but keeps checked state."}, t.addItems)
	mcp.AddTool(srv, &mcp.Tool{Name: "update_item", Description: "Check/uncheck, rename or move an item by id."}, t.updateItem)
	mcp.AddTool(srv, &mcp.Tool{Name: "post_briefing", Description: "Post the board's briefing in Markdown (shown in the Morning brief tile). Use source_id like \"brief-2026-10-12\" so a re-run replaces it."}, t.postBriefing)
	mcp.AddTool(srv, &mcp.Tool{Name: "push_inbox", Description: "Push triage candidates (emails, Jira issues, events) to the board's inbox. Re-pushing a source_id refreshes it but keeps its status."}, t.pushInbox)
	mcp.AddTool(srv, &mcp.Tool{Name: "set_widget", Description: "Replace a named widget's JSON payload. The dashboard draws two: " +
		"name \"jira\" with " + jiraShape + "; and name \"calendar\" with " + calendarShape + ". " +
		"Common variants (Jira's fields.summary/fields.status.name, ISO datetimes) are converted; " +
		"anything else is rejected with an explanation."}, t.setWidget)
	mcp.AddTool(srv, &mcp.Tool{Name: "propose_timeboxes", Description: "Replace the proposed timeboxes for one day on a board; they appear in the board's \"Your day\" tile. " +
		"date is YYYY-MM-DD (the board's today); each box has start/end as 24h HH:MM in the board time zone, " +
		"kind \"meeting\" (real calendar events), \"task\" (focus work; set item_ids to the to-do ids it covers) or " +
		"\"break\" (lunch, buffers), and a title. Boxes should not overlap. " +
		"People accept the plan on the dashboard; nothing is written to any calendar."}, t.proposeTimeboxes)
	mcp.AddTool(srv, &mcp.Tool{Name: "clear_board",
		Description: "DESTRUCTIVE, no undo: erase everything posted to a board (all list items, briefings, inbox, widgets, timeboxes). " +
			"Keeps the board, its lists and its connections. Only call this when the user explicitly asks to clear or erase that board. " +
			"confirm must be the board's exact name.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: ptr(true), IdempotentHint: true, OpenWorldHint: ptr(false)},
	}, t.clearBoard)
	return srv
}

func ptr[T any](v T) *T { return &v }

type mcpTools struct {
	s *Server
	p *principal
}

// resolveBoard accepts a board id ("1") or a case-insensitive name ("Work").
func (t *mcpTools) resolveBoard(ctx context.Context, ref string) (*db.Board, error) {
	ref = strings.TrimSpace(ref)
	boards, err := t.s.boards(ctx, t.p)
	if err != nil {
		return nil, err
	}
	if len(boards) == 1 && ref == "" {
		return &boards[0], nil
	}
	id, _ := strconv.ParseInt(ref, 10, 64)
	var names []string
	for i, b := range boards {
		if b.ID == id || strings.EqualFold(b.Name, ref) {
			return &boards[i], nil
		}
		names = append(names, b.Name)
	}
	return nil, fmt.Errorf("unknown board %q; available: %s", ref, strings.Join(names, ", "))
}

// resolveList finds a list by id, or by board + name (default "Today").
func (t *mcpTools) resolveList(ctx context.Context, listID int64, board, name string) (*db.List, *db.Board, error) {
	if listID != 0 {
		l, b, err := t.s.list(ctx, t.p, listID)
		if err != nil {
			return nil, nil, fmt.Errorf("unknown list_id %d", listID)
		}
		return l, b, nil
	}
	b, err := t.resolveBoard(ctx, board)
	if err != nil {
		return nil, nil, err
	}
	if name == "" {
		name = "Today"
	}
	l, err := t.s.store.ListByName(ctx, b.ID, name)
	if err != nil {
		return nil, nil, fmt.Errorf("board %s has no list %q; use list_lists or create_list", b.Name, name)
	}
	return l, b, nil
}

func toolErr(err error) error {
	var ie inputError
	switch {
	case errors.As(err, &ie):
		return ie
	case errors.Is(err, db.ErrInvalid):
		return errors.New("invalid input: check required fields and limits")
	case errors.Is(err, db.ErrNotFound):
		return errors.New("not found")
	case errors.Is(err, db.ErrConflict):
		return errors.New("conflict: the target list already has an item with this source_id")
	}
	return err
}

type boardArg struct {
	Board string `json:"board" jsonschema:"board name (e.g. Work) or id"`
}


type boardOut struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Timezone    string `json:"timezone"`
	HasSchedule bool   `json:"has_schedule"`
	Today       string `json:"today" jsonschema:"today's date in the board's time zone (YYYY-MM-DD)"`
}

func (t *mcpTools) listBoards(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, struct {
	Boards []boardOut `json:"boards"`
}, error) {
	var out struct {
		Boards []boardOut `json:"boards"`
	}
	boards, err := t.s.boards(ctx, t.p)
	if err != nil {
		return nil, out, err
	}
	out.Boards = []boardOut{}
	for _, b := range boards {
		out.Boards = append(out.Boards, boardOut{ID: b.ID, Name: b.Name, Timezone: b.Timezone, HasSchedule: b.HasSchedule,
			Today: db.StartOfDay(time.Now(), b.Timezone).Format("2006-01-02")})
	}
	return nil, out, nil
}

type listsOut struct {
	Lists []db.List `json:"lists"`
}

func (t *mcpTools) listLists(ctx context.Context, _ *mcp.CallToolRequest, in boardArg) (*mcp.CallToolResult, listsOut, error) {
	b, err := t.resolveBoard(ctx, in.Board)
	if err != nil {
		return nil, listsOut{}, err
	}
	lists, err := t.s.store.Lists(ctx, b.ID)
	return nil, listsOut{Lists: lists}, toolErr(err)
}

type createListIn struct {
	Board string `json:"board" jsonschema:"board name or id"`
	Name  string `json:"name" jsonschema:"list name"`
	Kind  string `json:"kind,omitempty" jsonschema:"daily (cleared each day, carries over) or standing (default)"`
}

type createListOut struct {
	List    db.List `json:"list"`
	Created bool    `json:"created"`
}

func (t *mcpTools) createList(ctx context.Context, _ *mcp.CallToolRequest, in createListIn) (*mcp.CallToolResult, createListOut, error) {
	b, err := t.resolveBoard(ctx, in.Board)
	if err != nil {
		return nil, createListOut{}, err
	}
	l, created, err := t.s.store.CreateList(ctx, b.ID, in.Name, in.Kind)
	if err != nil {
		return nil, createListOut{}, toolErr(err)
	}
	return nil, createListOut{List: *l, Created: created}, nil
}

type listRef struct {
	ListID int64  `json:"list_id,omitempty" jsonschema:"list id; or give board and list"`
	Board  string `json:"board,omitempty" jsonschema:"board name or id"`
	List   string `json:"list,omitempty" jsonschema:"list name on the board (default Today)"`
}

type itemsOut struct {
	Items []db.Item `json:"items"`
}

func (t *mcpTools) listOpenItems(ctx context.Context, _ *mcp.CallToolRequest, in listRef) (*mcp.CallToolResult, itemsOut, error) {
	l, _, err := t.resolveList(ctx, in.ListID, in.Board, in.List)
	if err != nil {
		return nil, itemsOut{}, err
	}
	items, err := t.s.store.Items(ctx, l.ID, true, time.Time{})
	return nil, itemsOut{Items: items}, toolErr(err)
}

type newItemIn struct {
	Title      string `json:"title"`
	Section    string `json:"section,omitempty" jsonschema:"optional grouping, e.g. Meetings or Follow-ups"`
	Due        string `json:"due,omitempty" jsonschema:"optional due date YYYY-MM-DD"`
	SourceType string `json:"source_type,omitempty" jsonschema:"jira, email, calendar, ..."`
	SourceID   string `json:"source_id,omitempty" jsonschema:"stable id at the source (Jira key, message id); makes posts idempotent"`
	SourceURL  string `json:"source_url,omitempty" jsonschema:"link back to the source"`
}

type addItemsIn struct {
	listRef
	Items []newItemIn `json:"items" jsonschema:"1-200 items"`
}

func (t *mcpTools) addItems(ctx context.Context, _ *mcp.CallToolRequest, in addItemsIn) (*mcp.CallToolResult, itemsOut, error) {
	l, _, err := t.resolveList(ctx, in.ListID, in.Board, in.List)
	if err != nil {
		return nil, itemsOut{}, err
	}
	if len(in.Items) == 0 || len(in.Items) > 200 {
		return nil, itemsOut{}, errors.New("items must have 1-200 entries")
	}
	news := make([]db.NewItem, len(in.Items))
	for i, it := range in.Items {
		if it.Due != "" && !db.ValidDate(it.Due) {
			return nil, itemsOut{}, fmt.Errorf("item %d: due must be YYYY-MM-DD", i)
		}
		news[i] = db.NewItem{Title: it.Title, Section: it.Section, Due: optional(it.Due),
			SourceType: it.SourceType, SourceID: it.SourceID, SourceURL: it.SourceURL}
	}
	items, err := t.s.store.AddItems(ctx, l.ID, news)
	return nil, itemsOut{Items: items}, toolErr(err)
}

type updateItemIn struct {
	ID     int64   `json:"id"`
	Done   *bool   `json:"done,omitempty"`
	Title  *string `json:"title,omitempty"`
	ListID *int64  `json:"list_id,omitempty" jsonschema:"move the item to this list"`
}

func (t *mcpTools) updateItem(ctx context.Context, req *mcp.CallToolRequest, in updateItemIn) (*mcp.CallToolResult, db.Item, error) {
	it, err := t.s.store.Item(ctx, in.ID)
	if err == nil {
		_, _, err = t.s.list(ctx, t.p, it.ListID)
	}
	if err == nil && in.ListID != nil {
		_, _, err = t.s.list(ctx, t.p, *in.ListID)
	}
	if err != nil {
		return nil, db.Item{}, toolErr(err)
	}
	updated, err := t.s.store.UpdateItem(ctx, in.ID, db.ItemPatch{Done: in.Done, Title: in.Title, ListID: in.ListID})
	if err != nil {
		return nil, db.Item{}, toolErr(err)
	}
	return nil, *updated, nil
}

type postBriefingIn struct {
	Board    string         `json:"board" jsonschema:"board name or id"`
	Markdown string         `json:"markdown" jsonschema:"the briefing in Markdown, up to 64 KB"`
	Stats    map[string]any `json:"stats,omitempty" jsonschema:"optional small numbers for tiles, e.g. {\"emails\":12,\"meetings\":3}"`
	SourceID string         `json:"source_id,omitempty" jsonschema:"e.g. brief-YYYY-MM-DD so a re-run replaces the day's brief"`
}

type briefingOut struct {
	ID        int64  `json:"id"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

func (t *mcpTools) postBriefing(ctx context.Context, _ *mcp.CallToolRequest, in postBriefingIn) (*mcp.CallToolResult, briefingOut, error) {
	b, err := t.resolveBoard(ctx, in.Board)
	if err != nil {
		return nil, briefingOut{}, err
	}
	var stats json.RawMessage
	if in.Stats != nil {
		stats, _ = json.Marshal(in.Stats)
	}
	br, err := t.s.store.PostBriefing(ctx, b.ID, db.NewBriefing{Markdown: in.Markdown, Stats: stats, SourceID: in.SourceID})
	if err != nil {
		return nil, briefingOut{}, toolErr(err)
	}
	return nil, briefingOut{ID: br.ID, CreatedAt: br.CreatedAt, UpdatedAt: br.UpdatedAt}, nil
}

type inboxItemIn struct {
	Type     string `json:"type,omitempty" jsonschema:"email, jira, calendar, ..."`
	Title    string `json:"title"`
	Snippet  string `json:"snippet,omitempty" jsonschema:"short excerpt, no full bodies"`
	URL      string `json:"url,omitempty" jsonschema:"link back to the source"`
	Reason   string `json:"reason,omitempty" jsonschema:"why it needs attention"`
	SourceID string `json:"source_id,omitempty" jsonschema:"stable id at the source; makes posts idempotent"`
}

type inboxIn struct {
	Board string        `json:"board" jsonschema:"board name or id"`
	Items []inboxItemIn `json:"items" jsonschema:"1-200 entries"`
}

type inboxOut struct {
	Items []db.InboxItem `json:"items"`
}

func (t *mcpTools) pushInbox(ctx context.Context, _ *mcp.CallToolRequest, in inboxIn) (*mcp.CallToolResult, inboxOut, error) {
	b, err := t.resolveBoard(ctx, in.Board)
	if err != nil {
		return nil, inboxOut{}, err
	}
	if len(in.Items) == 0 || len(in.Items) > 200 {
		return nil, inboxOut{}, errors.New("items must have 1-200 entries")
	}
	news := make([]db.NewInboxItem, len(in.Items))
	for i, it := range in.Items {
		news[i] = db.NewInboxItem(it)
	}
	items, err := t.s.store.PushInbox(ctx, b.ID, news)
	return nil, inboxOut{Items: items}, toolErr(err)
}

type widgetIn struct {
	Board   string `json:"board" jsonschema:"board name or id"`
	Name    string `json:"name" jsonschema:"lowercase name, e.g. calendar or jira"`
	Payload any    `json:"payload" jsonschema:"any JSON object or array, up to 256 KB"`
}

type widgetOut struct {
	Name      string `json:"name"`
	UpdatedAt string `json:"updated_at"`
}

func (t *mcpTools) setWidget(ctx context.Context, _ *mcp.CallToolRequest, in widgetIn) (*mcp.CallToolResult, widgetOut, error) {
	b, err := t.resolveBoard(ctx, in.Board)
	if err != nil {
		return nil, widgetOut{}, err
	}
	payload, err := json.Marshal(in.Payload)
	if err != nil || in.Payload == nil {
		return nil, widgetOut{}, errors.New("payload must be JSON")
	}
	payload, err = normalizeWidget(in.Name, payload, b.Timezone)
	if err != nil {
		return nil, widgetOut{}, toolErr(err)
	}
	wd, err := t.s.store.SetWidget(ctx, b.ID, in.Name, payload)
	if err != nil {
		return nil, widgetOut{}, toolErr(err)
	}
	return nil, widgetOut{Name: wd.Name, UpdatedAt: wd.UpdatedAt}, nil
}

type timeboxIn struct {
	Start   string  `json:"start" jsonschema:"HH:MM, 24h"`
	End     string  `json:"end" jsonschema:"HH:MM, 24h, after start"`
	Kind    string  `json:"kind" jsonschema:"task, meeting or break"`
	Title   string  `json:"title"`
	ItemIDs []int64 `json:"item_ids,omitempty" jsonschema:"ids of to-do items worked on in this box"`
}

type timeboxesIn struct {
	Board string      `json:"board" jsonschema:"board name or id"`
	Date  string      `json:"date" jsonschema:"YYYY-MM-DD in the board's time zone"`
	Boxes []timeboxIn `json:"boxes"`
}

type timeboxesOut struct {
	Timeboxes []db.Timebox `json:"timeboxes"`
}

func (t *mcpTools) proposeTimeboxes(ctx context.Context, _ *mcp.CallToolRequest, in timeboxesIn) (*mcp.CallToolResult, timeboxesOut, error) {
	b, err := t.resolveBoard(ctx, in.Board)
	if err != nil {
		return nil, timeboxesOut{}, err
	}
	news := make([]db.NewTimebox, len(in.Boxes))
	for i, bx := range in.Boxes {
		news[i] = db.NewTimebox(bx)
	}
	if err := checkTimeboxes(in.Date, news); err != nil {
		return nil, timeboxesOut{}, toolErr(err)
	}
	boxes, err := t.s.store.SetTimeboxes(ctx, b.ID, in.Date, news)
	if err != nil {
		return nil, timeboxesOut{}, toolErr(err)
	}
	slog.Info("timeboxes proposed", "board", b.ID, "date", in.Date, "count", len(boxes), "by", t.p.label)
	return nil, timeboxesOut{Timeboxes: boxes}, nil
}

type clearBoardIn struct {
	Board   string `json:"board" jsonschema:"board name or id"`
	Confirm string `json:"confirm" jsonschema:"the board's exact name, e.g. Work; required to prove the erase is intended"`
}

func (t *mcpTools) clearBoard(ctx context.Context, _ *mcp.CallToolRequest, in clearBoardIn) (*mcp.CallToolResult, db.ClearCounts, error) {
	b, err := t.resolveBoard(ctx, in.Board)
	if err != nil {
		return nil, db.ClearCounts{}, err
	}
	c, err := t.s.clearBoard(ctx, t.p, b, in.Confirm)
	if err != nil {
		return nil, db.ClearCounts{}, err
	}
	return nil, *c, nil
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
