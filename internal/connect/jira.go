package connect

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Grano22/aiculator/internal/meta"
)

// jira reads and updates Jira Cloud through its REST API v3.
type jira struct {
	*webClient
	src    *Source
	fields []string
}

func newJira(s *Source) (*jira, error) {
	token, err := envValue(s.Auth.TokenEnv, s.Name)
	if err != nil {
		return nil, err
	}
	email, err := envValue(s.Auth.EmailEnv, s.Name)
	if err != nil {
		return nil, err
	}
	w, err := newWebClient(s, s.URL, func(r *http.Request) {
		switch {
		case email != "" && token != "":
			r.SetBasicAuth(email, token)
		case token != "":
			r.Header.Set("Authorization", "Bearer "+token)
		}
	})
	if err != nil {
		return nil, err
	}
	return &jira{webClient: w, src: s, fields: jiraFields(s)}, nil
}

// jiraFields asks only for what the mapping uses, which makes a large search far cheaper.
func jiraFields(s *Source) []string {
	set := map[string]bool{"summary": true, "status": true, "updated": true, "attachment": true, "issuelinks": true}
	addSpec := func(f FieldSpec) {
		if f.From != "" {
			set[strings.SplitN(f.From, ".", 2)[0]] = true
		}
		for _, r := range f.Rules {
			for k := range r.When {
				set[strings.SplitN(k, ".", 2)[0]] = true
			}
		}
	}
	for _, f := range []*FieldSpec{s.Fields.Title, s.Fields.Spec, s.Fields.Priority, s.Fields.Tier, s.Fields.Tags, s.Fields.Status} {
		if f != nil {
			addSpec(*f)
		}
	}
	for _, f := range s.Fields.Labels {
		addSpec(f)
	}
	for _, f := range s.Fields.Props {
		addSpec(f)
	}
	for _, w := range s.SkipWhen {
		for k := range w {
			set[strings.SplitN(k, ".", 2)[0]] = true
		}
	}
	delete(set, "key") // The key is not a field; it comes with every issue.
	return slices.Sorted(maps.Keys(set))
}

type jiraIssue struct {
	Key    string         `json:"key"`
	Fields map[string]any `json:"fields"`
}

var orderBy = regexp.MustCompile(`(?i)\s+order\s+by\s+`)

// withSince narrows a query to recent changes. The JQL may end in ORDER BY, which must stay
// outside the parentheses. JQL dates have minute precision and use the account's time zone,
// so this reads a day further back than needed and lets the version check drop repeats.
func withSince(jql string, since time.Time) string {
	if since.IsZero() {
		return jql
	}
	clause := fmt.Sprintf(`updated >= "%s"`, since.UTC().Add(-24*time.Hour).Format("2006-01-02 15:04"))
	if loc := orderBy.FindStringIndex(jql); loc != nil {
		return "(" + jql[:loc[0]] + ") AND " + clause + jql[loc[0]:]
	}
	return "(" + jql + ") AND " + clause
}

// Pull implements Connector, using the enhanced search and its opaque page tokens.
func (j *jira) Pull(ctx context.Context, opts PullOptions, yield func(Item) error) error {
	jql := j.src.Query
	if opts.Query != "" {
		jql = opts.Query
	}
	jql = withSince(jql, opts.Since)
	next := ""
	for {
		body := map[string]any{"jql": jql, "maxResults": 100, "fields": j.fields}
		if next != "" {
			body["nextPageToken"] = next
		}
		var page struct {
			Issues        []jiraIssue `json:"issues"`
			NextPageToken string      `json:"nextPageToken"`
		}
		if err := j.do(ctx, http.MethodPost, "/rest/api/3/search/jql", body, &page); err != nil {
			return err
		}
		for _, issue := range page.Issues {
			if err := yield(j.toItem(issue)); err != nil {
				return err
			}
		}
		if page.NextPageToken == "" {
			return nil
		}
		next = page.NextPageToken
	}
}

// Get implements Getter.
func (j *jira) Get(ctx context.Context, key string) (Item, error) {
	var issue jiraIssue
	q := url.Values{"fields": {strings.Join(j.fields, ",")}}
	if err := j.do(ctx, http.MethodGet, "/rest/api/3/issue/"+url.PathEscape(key)+"?"+q.Encode(), nil, &issue); err != nil {
		return Item{}, err
	}
	return j.toItem(issue), nil
}

func (j *jira) toItem(issue jiraIssue) Item {
	f := issue.Fields
	if f == nil {
		f = map[string]any{}
	}
	fields := make(map[string]any, len(f)+1)
	for k, v := range f {
		fields[k] = plainField(v)
	}
	fields["key"] = issue.Key
	it := Item{
		Key: issue.Key, Fields: fields, URL: j.base + "/browse/" + issue.Key,
		Version: Text(f["updated"]), Status: nested(f["status"], "name"),
	}
	if atts, ok := f["attachment"].([]any); ok {
		for _, a := range atts {
			m, ok := a.(map[string]any)
			if !ok {
				continue
			}
			size, _ := m["size"].(float64)
			name := Text(m["filename"])
			it.Attachments = append(it.Attachments, Attachment{Name: name, Resource: meta.Resource{
				Kind: "jira.attachment", URL: Text(m["content"]), Name: name,
				MediaType: Text(m["mimeType"]), Size: int64(size), Connection: j.src.Name,
			}})
		}
	}
	// A link of type Blocks with an inward issue means that issue blocks this one.
	if links, ok := f["issuelinks"].([]any); ok {
		for _, l := range links {
			m, ok := l.(map[string]any)
			if !ok || nested(m["type"], "name") != "Blocks" {
				continue
			}
			if key := nested(m["inwardIssue"], "key"); key != "" {
				it.BlockedBy = append(it.BlockedBy, key)
			}
		}
	}
	return it
}

func nested(v any, key string) string {
	if m, ok := v.(map[string]any); ok {
		return Text(m[key])
	}
	return ""
}

// plainField turns rich text into plain text. API v3 sends descriptions in the Atlassian
// Document Format, and everything else stays as it came.
func plainField(v any) any {
	if m, ok := v.(map[string]any); ok && m["type"] == "doc" {
		var b strings.Builder
		adfText(m, &b)
		return strings.TrimSpace(b.String())
	}
	return v
}

func adfText(node map[string]any, b *strings.Builder) {
	if t, ok := node["text"].(string); ok {
		b.WriteString(t)
	}
	if node["type"] == "hardBreak" {
		b.WriteByte('\n')
	}
	if kids, ok := node["content"].([]any); ok {
		for _, k := range kids {
			if m, ok := k.(map[string]any); ok {
				adfText(m, b)
			}
		}
	}
	switch node["type"] {
	case "paragraph", "heading", "listItem", "codeBlock", "blockquote":
		b.WriteByte('\n')
	}
}

// SetStatus implements Pusher. Jira has no "set status": it moves an issue through a
// workflow, so this finds the transition that leads to the wanted status.
func (j *jira) SetStatus(ctx context.Context, key, status string) error {
	var out struct {
		Transitions []struct {
			ID string `json:"id"`
			To struct {
				Name string `json:"name"`
			} `json:"to"`
		} `json:"transitions"`
	}
	path := "/rest/api/3/issue/" + url.PathEscape(key) + "/transitions"
	if err := j.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return err
	}
	var names []string
	for _, t := range out.Transitions {
		names = append(names, t.To.Name)
		if strings.EqualFold(t.To.Name, status) {
			return j.do(ctx, http.MethodPost, path, map[string]any{"transition": map[string]string{"id": t.ID}}, nil)
		}
	}
	return fmt.Errorf("%w: %s cannot move to %q from where it is; it can move to: %s", ErrPermanent, key, status, strings.Join(names, ", "))
}

// VerifyHook implements HookReader.
func (j *jira) VerifyHook(secret string, h http.Header, body []byte) error {
	return verifySHA256(secret, h.Get("X-Hub-Signature"), body)
}

// HookKeys implements HookReader.
func (j *jira) HookKeys(body []byte) ([]string, error) {
	var ev struct {
		Issue struct {
			Key string `json:"key"`
		} `json:"issue"`
	}
	if err := json.Unmarshal(body, &ev); err != nil {
		return nil, err
	}
	if ev.Issue.Key == "" {
		return nil, nil
	}
	return []string{ev.Issue.Key}, nil
}
