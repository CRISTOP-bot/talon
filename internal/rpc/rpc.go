// Package rpc implements the newline-delimited JSON-RPC 2.0 client Talon uses to
// talk to external processes: plugins and MCP servers.
//
// The protocol is deliberately small: one JSON object per line on stdin and
// stdout, notifications for events and request/response pairs for calls.
package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
)

// Message is a JSON-RPC 2.0 frame.
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// IsRequest reports whether the message expects a response.
func (m *Message) IsRequest() bool { return m.Method != "" && len(m.ID) > 0 }

// IsNotification reports whether the message is a one-way event.
func (m *Message) IsNotification() bool { return m.Method != "" && len(m.ID) == 0 }

// IsResponse reports whether the message answers a request.
func (m *Message) IsResponse() bool { return m.Method == "" && len(m.ID) > 0 }

// Error is a JSON-RPC error object.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *Error) Error() string {
	if e.Data != nil {
		return fmt.Sprintf("%s (code %d): %v", e.Message, e.Code, e.Data)
	}
	return fmt.Sprintf("%s (code %d)", e.Message, e.Code)
}

// Standard JSON-RPC error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// Handler answers requests coming from the peer.
type Handler func(ctx context.Context, method string, params json.RawMessage) (any, error)

// Client is a JSON-RPC client over an io.ReadWriter pair (usually a process's
// stdin/stdout).
type Client struct {
	rw     io.ReadWriter
	encMu  sync.Mutex
	enc    *json.Encoder
	nextID atomic.Int64

	mu       sync.Mutex
	pending  map[string]chan *Message
	closed   bool
	closeErr error

	handler Handler
	done    chan struct{}
	wg      sync.WaitGroup
	// CallTimeout bounds a single request; zero means wait for the context.
	CallTimeout time.Duration
	// OnLog receives diagnostic lines (stderr, protocol errors).
	OnLog func(format string, args ...any)
}

// NewClient creates a client over rw and starts the reader goroutine.
func NewClient(rw io.ReadWriter, handler Handler) *Client {
	c := &Client{
		rw:      rw,
		enc:     json.NewEncoder(rw),
		pending: map[string]chan *Message{},
		handler: handler,
		done:    make(chan struct{}),
	}
	c.wg.Add(1)
	go c.readLoop()
	return c
}

func (c *Client) logf(format string, args ...any) {
	if c.OnLog != nil {
		c.OnLog(format, args...)
	}
}

// readLoop dispatches incoming frames.
func (c *Client) readLoop() {
	defer c.wg.Done()
	sc := bufio.NewScanner(c.rw)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg Message
		if err := json.Unmarshal(line, &msg); err != nil {
			c.logf("rpc: cannot decode message: %v", err)
			continue
		}
		msgCopy := msg
		switch {
		case msg.IsResponse():
			c.deliver(&msgCopy)
		case msg.IsRequest():
			c.serve(&msgCopy)
		case msg.IsNotification():
			if c.handler != nil {
				// Notifications are handled best-effort on a goroutine.
				go func(m Message) {
					if _, err := c.handler(context.Background(), m.Method, m.Params); err != nil {
						c.logf("rpc: notification %s failed: %v", m.Method, err)
					}
				}(msgCopy)
			}
		}
	}
	c.failAll()
	close(c.done)
}

// deliver routes a response to its waiting caller.
func (c *Client) deliver(msg *Message) {
	key := string(msg.ID)
	c.mu.Lock()
	ch, ok := c.pending[key]
	delete(c.pending, key)
	c.mu.Unlock()
	if ok {
		ch <- msg
	}
}

func (c *Client) failAll() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	for k, ch := range c.pending {
		ch <- &Message{Error: &Error{Code: CodeInternalError, Message: "connection closed"}}
		delete(c.pending, k)
	}
	c.mu.Unlock()
}

// serve answers an incoming request.
func (c *Client) serve(msg *Message) {
	if c.handler == nil {
		_ = c.write(Message{
			JSONRPC: "2.0",
			ID:      msg.ID,
			Error:   &Error{Code: CodeMethodNotFound, Message: "no handler installed"},
		})
		return
	}
	result, err := c.handler(context.Background(), msg.Method, msg.Params)
	reply := Message{JSONRPC: "2.0", ID: msg.ID}
	if err != nil {
		reply.Error = &Error{Code: CodeInternalError, Message: err.Error()}
	} else {
		encoded, merr := json.Marshal(result)
		if merr != nil {
			reply.Error = &Error{Code: CodeInternalError, Message: merr.Error()}
		} else {
			reply.Result = encoded
		}
	}
	_ = c.write(reply)
}

// Notify sends a one-way message.
func (c *Client) Notify(method string, params any) error {
	encoded, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return c.write(Message{JSONRPC: "2.0", Method: method, Params: encoded})
}

// Call performs a request and decodes the result into out.
func (c *Client) Call(ctx context.Context, method string, params any, out any) error {
	if c.CallTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.CallTimeout)
		defer cancel()
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return err
	}
	id := c.nextID.Add(1)
	rawID, _ := json.Marshal(id)
	ch := make(chan *Message, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return fmt.Errorf("connection closed")
	}
	c.pending[string(rawID)] = ch
	c.mu.Unlock()

	if err := c.write(Message{JSONRPC: "2.0", ID: rawID, Method: method, Params: encoded}); err != nil {
		c.mu.Lock()
		delete(c.pending, string(rawID))
		c.mu.Unlock()
		return err
	}
	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, string(rawID))
		c.mu.Unlock()
		return ctx.Err()
	case msg := <-ch:
		if msg.Error != nil {
			return msg.Error
		}
		if out == nil {
			return nil
		}
		if len(msg.Result) == 0 {
			return nil
		}
		return json.Unmarshal(msg.Result, out)
	}
}

// write sends a frame.
func (c *Client) write(msg Message) error {
	msg.JSONRPC = "2.0"
	c.encMu.Lock()
	defer c.encMu.Unlock()
	if err := c.enc.Encode(msg); err != nil {
		return fmt.Errorf("cannot write to the peer: %w", err)
	}
	return nil
}

// Close marks the client as closed and fails every pending call. It never
// blocks: the reader loop ends when the underlying stream is closed by its
// owner (usually the peer process exiting). Use Wait when you need to join it.
func (c *Client) Close() {
	c.mu.Lock()
	c.closed = true
	pending := c.pending
	c.pending = map[string]chan *Message{}
	c.mu.Unlock()
	for _, ch := range pending {
		ch <- &Message{Error: &Error{Code: CodeInternalError, Message: "connection closed"}}
	}
}

// Wait blocks until the reader loop has finished, which requires the underlying
// stream to be closed.
func (c *Client) Wait() { c.wg.Wait() }

// Done is closed when the reader loop has finished.
func (c *Client) Done() <-chan struct{} { return c.done }
