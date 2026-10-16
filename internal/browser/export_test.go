package browser

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// SetCookiesForTest plants cookies through the browser-level Storage domain.
// A Domain with a leading dot makes a domain cookie; without one, a host-only
// cookie for that host.
func (b *Browser) SetCookiesForTest(ctx context.Context, cs []Cookie) error {
	type param struct {
		Name         string          `json:"name"`
		Value        string          `json:"value"`
		URL          string          `json:"url,omitempty"`
		Domain       string          `json:"domain,omitempty"`
		Path         string          `json:"path,omitempty"`
		Secure       bool            `json:"secure"`
		Expires      float64         `json:"expires,omitempty"`
		SameSite     string          `json:"sameSite,omitempty"`
		PartitionKey json.RawMessage `json:"partitionKey,omitempty"`
	}
	ps := make([]param, 0, len(cs))
	for _, c := range cs {
		p := param{Name: c.Name, Value: c.Value, Path: c.Path, Secure: true, Expires: c.Expires, PartitionKey: c.PartitionKey}
		if len(c.PartitionKey) > 0 {
			p.SameSite = "None" // partitioned cookies must be cross-site capable
		}
		if strings.HasPrefix(c.Domain, ".") {
			p.Domain = c.Domain
		} else {
			p.URL = "https://" + c.Domain + "/"
		}
		ps = append(ps, p)
	}
	return b.conn.call(ctx, "", "Storage.setCookies", map[string]any{"cookies": ps}, nil)
}

// KillForTest kills the browser process only, as a crash would.
func (b *Browser) KillForTest() error { return b.cmd.Process.Kill() }

// CallForTest sends a raw command on the page session.
func (b *Browser) CallForTest(ctx context.Context, method string, params any) error {
	return b.conn.call(ctx, b.session, method, params, nil)
}

// BuildArgs exposes the Chromium command line for inspection.
var BuildArgs = buildArgs

// SetAttachTimeout shortens the attach bound and returns a restore func.
func SetAttachTimeout(d time.Duration) (restore func()) {
	old := attachTimeout
	attachTimeout = d
	return func() { attachTimeout = old }
}

// EnsurePrefs exposes the profile Preferences merge.
var EnsurePrefs = ensurePrefs

// SysProcAttr exposes the launch process attributes.
var SysProcAttr = sysProcAttr
