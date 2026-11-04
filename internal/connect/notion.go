package connect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Grano22/aiculator/internal/meta"
)

const defaultNotionVersion = "2026-03-11"

// notion reads and updates pages of a Notion data source.
type notion struct {
	*webClient
	src *Source
}

func newNotion(s *Source) (*notion, error) {
	token, err := envValue(s.Auth.TokenEnv, s.Name)
	if err != nil {
		return nil, err
	}
	version := s.APIVersion
	if version == "" {
		version = defaultNotionVersion
	}
	base := s.URL
	if base == "" {
		base = "https://api.notion.com"
	}
	w, err := newWebClient(s, base, func(r *http.Request) {
		r.Header.Set("Notion-Version", version)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
	})
	if err != nil {
		return nil, err
	}
	return &notion{webClient: w, src: s}, nil
}

type notionPage struct {
	ID             string         `json:"id"`
	URL            string         `json:"url"`
	LastEditedTime string         `json:"last_edited_time"`
	InTrash        bool           `json:"in_trash"`
	Properties     map[string]any `json:"properties"`
}

// Pull implements Connector.
func (n *notion) Pull(ctx context.Context, opts PullOptions, yield func(Item) error) error {
	var filter any = n.src.Filter
	if opts.Query != "" {
		var parsed any
		if err := json.Unmarshal([]byte(opts.Query), &parsed); err != nil {
			return fmt.Errorf("a Notion query is a JSON filter: %w", err)
		}
		filter = parsed
	}
	if !opts.Since.IsZero() {
		recent := map[string]any{"timestamp": "last_edited_time", "last_edited_time": map[string]any{
			"on_or_after": opts.Since.UTC().Add(-time.Minute).Format(time.RFC3339),
		}}
		if filter == nil {
			filter = recent
		} else {
			filter = map[string]any{"and": []any{filter, recent}}
		}
	}
	cursor := ""
	for {
		body := map[string]any{"page_size": 100}
		if filter != nil {
			body["filter"] = filter
		}
		if cursor != "" {
			body["start_cursor"] = cursor
		}
		var page struct {
			Results    []notionPage `json:"results"`
			HasMore    bool         `json:"has_more"`
			NextCursor *string      `json:"next_cursor"`
		}
		path := "/v1/data_sources/" + url.PathEscape(n.src.DataSource) + "/query"
		if err := n.do(ctx, http.MethodPost, path, body, &page); err != nil {
			return err
		}
		for _, p := range page.Results {
			it, err := n.toItem(ctx, p)
			if err != nil {
				return err
			}
			if err := yield(it); err != nil {
				return err
			}
		}
		if !page.HasMore || page.NextCursor == nil || *page.NextCursor == "" {
			return nil
		}
		cursor = *page.NextCursor
	}
}

// Get implements Getter.
func (n *notion) Get(ctx context.Context, key string) (Item, error) {
	var p notionPage
	if err := n.do(ctx, http.MethodGet, "/v1/pages/"+url.PathEscape(key), nil, &p); err != nil {
		return Item{}, err
	}
	return n.toItem(ctx, p)
}

func (n *notion) toItem(ctx context.Context, p notionPage) (Item, error) {
	fields := make(map[string]any, len(p.Properties)+2)
	it := Item{Key: p.ID, Version: p.LastEditedTime, URL: p.URL, Fields: fields}
	for name, raw := range p.Properties {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := m["type"].(string)
		v := notionValue(kind, m[kind])
		if kind == "files" {
			it.Attachments = append(it.Attachments, n.notionFiles(m[kind])...)
		}
		fields[name] = v
		if kind == "status" {
			it.Status = Text(v)
		}
	}
	fields["id"], fields["url"] = p.ID, p.URL
	if p.InTrash {
		it.Status = "in_trash"
	}
	if n.src.FetchBody {
		body, err := n.pageText(ctx, p.ID)
		if err != nil {
			return Item{}, err
		}
		fields["body"] = body
	}
	return it, nil
}

// notionValue reduces a property to plain data, whatever its type.
func notionValue(kind string, v any) any {
	switch kind {
	case "title", "rich_text":
		list, _ := v.([]any)
		var b strings.Builder
		for _, e := range list {
			if m, ok := e.(map[string]any); ok {
				b.WriteString(Text(m["plain_text"]))
			}
		}
		return b.String()
	case "select", "status":
		return nested(v, "name")
	case "multi_select", "people":
		list, _ := v.([]any)
		out := make([]string, 0, len(list))
		for _, e := range list {
			out = append(out, nested(e, "name"))
		}
		return out
	case "date":
		return nested(v, "start")
	case "relation":
		list, _ := v.([]any)
		out := make([]string, 0, len(list))
		for _, e := range list {
			out = append(out, nested(e, "id"))
		}
		return out
	case "formula", "rollup":
		if m, ok := v.(map[string]any); ok {
			inner, _ := m["type"].(string)
			return notionValue(inner, m[inner])
		}
	case "files":
		return nil // These become attachments instead.
	}
	return v
}

func (n *notion) notionFiles(v any) []Attachment {
	list, _ := v.([]any)
	var out []Attachment
	for _, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := m["type"].(string)
		link := nested(m[kind], "url")
		if link == "" {
			continue
		}
		name := Text(m["name"])
		// A file Notion hosts has a link that expires after about an hour, and needs no
		// token, so it goes through the guarded fetcher like any other public URL.
		out = append(out, Attachment{Name: name, Resource: meta.Resource{Kind: "notion.file", URL: link, Name: name}})
	}
	return out
}

const maxBodyPages = 20

// pageText reads a page's text blocks. It costs a request per page, so it is opt-in.
func (n *notion) pageText(ctx context.Context, id string) (string, error) {
	var b strings.Builder
	cursor := ""
	for range maxBodyPages {
		q := url.Values{"page_size": {"100"}}
		if cursor != "" {
			q.Set("start_cursor", cursor)
		}
		var page struct {
			Results    []map[string]any `json:"results"`
			HasMore    bool             `json:"has_more"`
			NextCursor *string          `json:"next_cursor"`
		}
		if err := n.do(ctx, http.MethodGet, "/v1/blocks/"+url.PathEscape(id)+"/children?"+q.Encode(), nil, &page); err != nil {
			return "", err
		}
		for _, block := range page.Results {
			kind, _ := block["type"].(string)
			inner, _ := block[kind].(map[string]any)
			if inner == nil {
				continue
			}
			if text := Text(notionValue("rich_text", inner["rich_text"])); text != "" {
				b.WriteString(text)
				b.WriteByte('\n')
			}
		}
		if !page.HasMore || page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	return strings.TrimSpace(b.String()), nil
}

// SetStatus implements Pusher. The property is named by the mapping's status field, and
// must be of Notion's status type.
func (n *notion) SetStatus(ctx context.Context, key, status string) error {
	prop := "Status"
	if f := n.src.Fields.Status; f != nil && f.From != "" {
		prop = f.From
	}
	body := map[string]any{"properties": map[string]any{prop: map[string]any{"status": map[string]string{"name": status}}}}
	return n.do(ctx, http.MethodPatch, "/v1/pages/"+url.PathEscape(key), body, nil)
}

// VerifyHook implements HookReader.
func (n *notion) VerifyHook(secret string, h http.Header, body []byte) error {
	return verifySHA256(secret, h.Get("X-Notion-Signature"), body)
}

// HookKeys implements HookReader. Notion's events name a page and carry no content.
func (n *notion) HookKeys(body []byte) ([]string, error) {
	var ev struct {
		Type   string `json:"type"`
		Entity struct {
			ID   string `json:"id"`
			Type string `json:"type"`
		} `json:"entity"`
	}
	if err := json.Unmarshal(body, &ev); err != nil {
		return nil, err
	}
	if ev.Entity.ID == "" {
		return nil, errors.New("event names no entity")
	}
	if ev.Entity.Type != "page" {
		return nil, nil
	}
	return []string{ev.Entity.ID}, nil
}
