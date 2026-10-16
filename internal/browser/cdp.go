package browser

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// conn speaks CDP over the --remote-debugging-pipe fds: JSON messages, each
// terminated by a NUL byte. Responses are matched by id; events are ignored.
// Errors carry the method and Chromium's error text, never params or results,
// because both can hold cookie values.
type conn struct {
	w *os.File // Chromium reads this on fd 3
	r *os.File // Chromium writes this on fd 4

	wmu sync.Mutex // serialises writes to w

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan response

	readerDone chan struct{}   // closed when r returns EOF or an error
	exited     <-chan struct{} // closed when the browser process exits
}

type request struct {
	ID        int64  `json:"id"`
	SessionID string `json:"sessionId,omitempty"`
	Method    string `json:"method"`
	Params    any    `json:"params"`
}

type response struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *cdpError       `json:"error"`
}

type cdpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func newConn(w, r *os.File, exited <-chan struct{}) *conn {
	c := &conn{
		w:          w,
		r:          r,
		pending:    make(map[int64]chan response),
		readerDone: make(chan struct{}),
		exited:     exited,
	}
	go c.read()
	return c
}

func (c *conn) read() {
	defer close(c.readerDone)
	br := bufio.NewReader(c.r)
	for {
		msg, err := br.ReadBytes(0)
		if err != nil {
			return
		}
		var resp response
		if json.Unmarshal(msg[:len(msg)-1], &resp) != nil || resp.ID == 0 {
			continue // events carry no id
		}
		c.mu.Lock()
		ch := c.pending[resp.ID]
		delete(c.pending, resp.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- resp // buffered; the caller may have gone
		}
	}
}

// call sends method on sessionID ("" for the browser target) and decodes the
// result into out when out is non-nil. It returns when the response arrives,
// ctx ends, or the browser goes away; the last wraps ErrExited.
func (c *conn) call(ctx context.Context, sessionID, method string, params, out any) error {
	if params == nil {
		params = struct{}{}
	}
	ch := make(chan response, 1)
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	msg, err := json.Marshal(request{ID: id, SessionID: sessionID, Method: method, Params: params})
	if err != nil {
		return fmt.Errorf("cdp %s: encode: %w", method, err)
	}
	if err := c.write(append(msg, 0)); err != nil {
		// The only reader of this pipe is the browser.
		return fmt.Errorf("cdp %s: %w: %w", method, ErrExited, err)
	}

	select {
	case resp := <-ch:
		return decode(method, resp, out)
	case <-ctx.Done():
		return fmt.Errorf("cdp %s: %w", method, ctx.Err())
	case <-c.readerDone:
	case <-c.exited:
	}
	// A response read just before the pipe closed still counts.
	select {
	case resp := <-ch:
		return decode(method, resp, out)
	default:
		return fmt.Errorf("cdp %s: %w", method, ErrExited)
	}
}

func decode(method string, resp response, out any) error {
	if resp.Error != nil {
		return fmt.Errorf("cdp %s: %s (%d)", method, resp.Error.Message, resp.Error.Code)
	}
	if out != nil {
		if err := json.Unmarshal(resp.Result, out); err != nil {
			return fmt.Errorf("cdp %s: decode result: %w", method, err)
		}
	}
	return nil
}

func (c *conn) write(msg []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err := c.w.Write(msg)
	return err
}
