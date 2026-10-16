package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// metaNames are the only cookies fbm ever reads. Everything else the browser
// holds is dropped before a Cookie is built.
var metaNames = map[string]bool{"c_user": true, "xs": true, "datr": true}

// Cookie is one browser cookie. PartitionKey is opaque and is passed back to
// Chromium exactly as received.
type Cookie struct {
	Name         string          `json:"name"`
	Value        string          `json:"value"`
	Domain       string          `json:"domain"`
	Path         string          `json:"path"`
	Expires      float64         `json:"expires"`
	Session      bool            `json:"session"`
	PartitionKey json.RawMessage `json:"partitionKey,omitempty"`
}

// String identifies the cookie and redacts its value.
func (c Cookie) String() string {
	return fmt.Sprintf("%s@%s%s=<redacted len %d>", c.Name, c.Domain, c.Path, len(c.Value))
}

// Format makes every fmt verb, %#v included, print String.
func (c Cookie) Format(f fmt.State, _ rune) { io.WriteString(f, c.String()) }

// Cookies returns the c_user, xs and datr cookies on facebook.com or any
// subdomain, from every partition of the browser's cookie store.
func (b *Browser) Cookies(ctx context.Context) ([]Cookie, error) {
	var res struct {
		Cookies []json.RawMessage `json:"cookies"`
	}
	if err := b.conn.call(ctx, "", "Storage.getCookies", nil, &res); err != nil {
		return nil, err
	}
	var out []Cookie
	for _, raw := range res.Cookies {
		// Decode the identity first so other cookies' values are never copied out.
		var id struct {
			Name   string `json:"name"`
			Domain string `json:"domain"`
		}
		if err := json.Unmarshal(raw, &id); err != nil {
			return nil, fmt.Errorf("decode cookie: %w", err)
		}
		if !metaNames[id.Name] || !isFacebook(id.Domain) {
			continue
		}
		var c Cookie
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, fmt.Errorf("decode cookie %s: %w", id.Name, err)
		}
		out = append(out, c)
	}
	return out, nil
}

// DeleteCookies deletes every facebook.com-family cookie with one of names
// by its exact name, domain, path and partition, then re-reads the store and
// fails if any is left. A name other than c_user, xs or datr is an error.
func (b *Browser) DeleteCookies(ctx context.Context, names ...string) (int, error) {
	want := make(map[string]bool, len(names))
	for _, n := range names {
		if !metaNames[n] {
			return 0, fmt.Errorf("delete cookies: %q is not c_user, xs or datr", n)
		}
		want[n] = true
	}
	matching := func() ([]Cookie, error) {
		cs, err := b.Cookies(ctx)
		if err != nil {
			return nil, err
		}
		var m []Cookie
		for _, c := range cs {
			if want[c.Name] {
				m = append(m, c)
			}
		}
		return m, nil
	}

	cs, err := matching()
	if err != nil {
		return 0, err
	}
	deleted := 0
	for _, c := range cs {
		params := map[string]any{"name": c.Name, "domain": c.Domain, "path": c.Path}
		if len(c.PartitionKey) > 0 {
			params["partitionKey"] = c.PartitionKey
		}
		if err := b.conn.call(ctx, b.session, "Network.deleteCookies", params, nil); err != nil {
			return deleted, err
		}
		deleted++
	}

	left, err := matching()
	if err != nil {
		return deleted, err
	}
	if len(left) > 0 {
		var ids []string
		for _, c := range left {
			ids = append(ids, c.Name+"@"+c.Domain+c.Path)
		}
		return deleted, fmt.Errorf("delete cookies: %d still present: %s", len(left), strings.Join(ids, ", "))
	}
	return deleted, nil
}

// isFacebook reports whether a cookie domain, with or without its leading
// dot, is facebook.com or a subdomain of it.
func isFacebook(domain string) bool {
	d := strings.TrimPrefix(strings.ToLower(domain), ".")
	return d == "facebook.com" || strings.HasSuffix(d, ".facebook.com")
}
