// Package auth turns a browser cookie export into messagix cookies.
package auth

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.mau.fi/mautrix-meta/pkg/messagix/cookies"
	"go.mau.fi/mautrix-meta/pkg/messagix/types"

	"github.com/karmiphuc/chatgpt-dots-fb-messenger-cli/internal/browser"
)

var ErrMissingCookies = errors.New("missing required cookies")

const (
	httpOnlyPrefix = "#HttpOnly_"
	metaDomain     = "facebook.com"
)

// now is the clock used for expiry checks; tests override it.
var now = time.Now

// entry is one cookie from an export. A zero expires means no expiry (session).
type entry struct {
	name, value, domain string
	expires             time.Time
}

// keep reports whether the entry is a live cookie on facebook.com. An entry
// without a domain (the {"name":"value"} JSON shape) is kept.
func (e entry) keep() bool {
	if !e.expires.IsZero() && e.expires.Before(now()) {
		return false
	}
	d := strings.ToLower(strings.TrimPrefix(e.domain, "."))
	return d == "" || d == metaDomain || strings.HasSuffix(d, "."+metaDomain)
}

// metaCookieNames is the required set plus the optional names messagix reads.
var metaCookieNames = func() map[cookies.MetaCookieName]struct{} {
	names := append([]cookies.MetaCookieName{
		cookies.FBCookieSB,
		cookies.FBCookieFR,
		cookies.FBCookieWindowDimensions,
		cookies.FBCookiePresence,
		cookies.FBCookieOO,
		cookies.MetaCookieDevicePixelRatio,
	}, cookies.FBRequiredCookies...)
	set := make(map[cookies.MetaCookieName]struct{}, len(names))
	for _, n := range names {
		set[n] = struct{}{}
	}
	return set
}()

// ParseCookieExport reads a Chrome-extension JSON export or a Netscape
// cookies.txt and returns the Meta cookies it contains. The format is chosen
// by the first non-space byte: '[' or '{' is JSON, anything else is Netscape.
func ParseCookieExport(r io.Reader) (*cookies.Cookies, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read cookie export: %w", err)
	}

	var entries []entry
	if trimmed := bytes.TrimSpace(data); len(trimmed) > 0 && (trimmed[0] == '[' || trimmed[0] == '{') {
		entries, err = parseJSON(trimmed)
	} else {
		entries, err = parseNetscape(data)
	}
	if err != nil {
		return nil, err
	}

	values := make(map[cookies.MetaCookieName]string)
	for _, e := range entries {
		if _, ok := metaCookieNames[cookies.MetaCookieName(e.name)]; ok && e.keep() {
			values[cookies.MetaCookieName(e.name)] = e.value
		}
	}

	return facebookCookies(values)
}

// facebookCookies wraps values as Facebook cookies, failing with
// ErrMissingCookies and the missing names when a required one is absent.
func facebookCookies(values map[cookies.MetaCookieName]string) (*cookies.Cookies, error) {
	c := &cookies.Cookies{Platform: types.Facebook}
	c.UpdateValues(values)
	if missing := c.GetMissingCookieNames(); len(missing) > 0 {
		names := make([]string, len(missing))
		for i, n := range missing {
			names[i] = string(n)
		}
		return nil, fmt.Errorf("%w: %s", ErrMissingCookies, strings.Join(names, ", "))
	}
	return c, nil
}

// unixTime converts fractional Unix seconds, as exports and CDP give them.
func unixTime(sec float64) time.Time {
	whole, frac := math.Modf(sec)
	return time.Unix(int64(whole), int64(frac*1e9))
}

// FromBrowser builds messagix cookies from the browser's c_user, xs and datr.
// Per name it takes an unpartitioned, unexpired cookie on .facebook.com or
// facebook.com, else one on any facebook.com subdomain; a name present only in
// a partition counts as missing. c_user must be a positive user id. Error text
// names cookies, never values.
func FromBrowser(cs []browser.Cookie) (*cookies.Cookies, error) {
	values := make(map[cookies.MetaCookieName]string)
	preferred := make(map[cookies.MetaCookieName]bool)
	for _, bc := range cs {
		name := cookies.MetaCookieName(bc.Name)
		if !slices.Contains(cookies.FBRequiredCookies, name) || isPartitioned(bc.PartitionKey) {
			continue
		}
		e := entry{name: bc.Name, value: bc.Value, domain: bc.Domain}
		if !bc.Session && bc.Expires > 0 {
			e.expires = unixTime(bc.Expires)
		}
		// keep also lets a domainless entry through; a browser cookie always has one.
		if bc.Domain == "" || bc.Value == "" || !e.keep() || preferred[name] {
			continue
		}
		// The first .facebook.com cookie wins; until one is seen, the first
		// subdomain cookie stands in.
		if _, seen := values[name]; seen && !isMetaDomainCookie(bc.Domain) {
			continue
		}
		values[name] = bc.Value
		preferred[name] = isMetaDomainCookie(bc.Domain)
	}
	if id, err := strconv.ParseInt(values[cookies.FBCookieCUser], 10, 64); err != nil || id <= 0 {
		delete(values, cookies.FBCookieCUser)
	}

	return facebookCookies(values)
}

// isPartitioned reports whether a CDP partitionKey names a partition. Absent
// and JSON null both mean the unpartitioned jar.
func isPartitioned(key json.RawMessage) bool {
	k := bytes.TrimSpace(key)
	return len(k) > 0 && !bytes.Equal(k, []byte("null"))
}

// isMetaDomainCookie reports whether domain is facebook.com itself, with or
// without the leading dot, rather than a subdomain.
func isMetaDomainCookie(domain string) bool {
	return strings.ToLower(strings.TrimPrefix(domain, ".")) == metaDomain
}

// parseJSON accepts [{"name","value","domain","expirationDate","session"}]
// or {"name": "value"}.
func parseJSON(data []byte) ([]entry, error) {
	if data[0] == '{' {
		var pairs map[string]string
		if err := json.Unmarshal(data, &pairs); err != nil {
			return nil, fmt.Errorf("parse cookie JSON object: %w", err)
		}
		entries := make([]entry, 0, len(pairs))
		for name, value := range pairs {
			entries = append(entries, entry{name: name, value: value})
		}
		return entries, nil
	}
	var raw []struct {
		Name           string   `json:"name"`
		Value          string   `json:"value"`
		Domain         string   `json:"domain"`
		ExpirationDate *float64 `json:"expirationDate"`
		Session        bool     `json:"session"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse cookie JSON array: %w", err)
	}
	entries := make([]entry, 0, len(raw))
	for _, r := range raw {
		e := entry{name: r.Name, value: r.Value, domain: r.Domain}
		if !r.Session && r.ExpirationDate != nil && *r.ExpirationDate > 0 {
			e.expires = unixTime(*r.ExpirationDate)
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// parseNetscape reads seven tab-separated fields per line:
// domain, flag, path, secure, expiry (unix seconds, 0 = session), name, value.
func parseNetscape(data []byte) ([]entry, error) {
	var entries []entry
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		line = strings.TrimPrefix(line, httpOnlyPrefix)
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.SplitN(line, "\t", 7)
		if len(fields) != 7 {
			continue
		}
		e := entry{name: fields[5], value: fields[6], domain: fields[0]}
		if sec, err := strconv.ParseInt(fields[4], 10, 64); err == nil && sec > 0 {
			e.expires = time.Unix(sec, 0)
		}
		entries = append(entries, e)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("parse Netscape cookies: %w", err)
	}
	return entries, nil
}
