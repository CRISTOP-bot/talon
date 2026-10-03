package rpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// pipePair returns two connected net.Conn values so a client and a server can
// talk over a real stream.
func pipePair(t *testing.T) (client, server net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	type result struct {
		conn net.Conn
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := ln.Accept()
		ch <- result{c, err}
	}()
	client, err = net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	r := <-ch
	if r.err != nil {
		t.Fatal(r.err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		_ = r.conn.Close()
	})
	return client, r.conn
}

func TestClientCallRoundTrip(t *testing.T) {
	c, s := pipePair(t)

	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		sc := bufio.NewScanner(s)
		enc := json.NewEncoder(s)
		for sc.Scan() {
			var msg Message
			if err := json.Unmarshal(sc.Bytes(), &msg); err != nil {
				continue
			}
			_ = enc.Encode(Message{
				JSONRPC: "2.0",
				ID:      msg.ID,
				Result:  json.RawMessage(`{"answer":42}`),
			})
		}
	}()

	client := NewClient(c, nil)
	defer client.Close()
	var out struct {
		Answer int `json:"answer"`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Call(ctx, "add", map[string]int{"a": 40, "b": 2}, &out); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if out.Answer != 42 {
		t.Errorf("answer = %d", out.Answer)
	}
	_ = s.Close()
	<-serverDone
}

func TestClientReportsServerErrors(t *testing.T) {
	c, s := pipePair(t)
	go func() {
		sc := bufio.NewScanner(s)
		enc := json.NewEncoder(s)
		for sc.Scan() {
			var msg Message
			_ = json.Unmarshal(sc.Bytes(), &msg)
			_ = enc.Encode(Message{
				JSONRPC: "2.0",
				ID:      msg.ID,
				Error:   &Error{Code: CodeInvalidParams, Message: "bad params"},
			})
		}
	}()
	client := NewClient(c, nil)
	defer client.Close()
	err := client.Call(context.Background(), "f", nil, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "bad params") {
		t.Errorf("error = %v", err)
	}
	var rpcErr *Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != CodeInvalidParams {
		t.Errorf("error should be a *rpc.Error, got %#v", err)
	}
	_ = s.Close()
}

func TestClientServesPeerRequests(t *testing.T) {
	c, s := pipePair(t)
	client := NewClient(c, func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		if method == "greet" {
			return map[string]string{"hello": "world"}, nil
		}
		return nil, errors.New("unknown method")
	})
	defer client.Close()

	sc := bufio.NewScanner(s)
	req, _ := json.Marshal(Message{JSONRPC: "2.0", ID: json.RawMessage(`7`), Method: "greet"})
	if _, err := s.Write(append(req, '\n')); err != nil {
		t.Fatal(err)
	}
	if !sc.Scan() {
		t.Fatal("no response")
	}
	var resp Message
	if err := json.Unmarshal(sc.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if string(resp.ID) != "7" {
		t.Errorf("id = %s", resp.ID)
	}
	if !strings.Contains(string(resp.Result), "world") {
		t.Errorf("result = %s", resp.Result)
	}
}

func TestNotifyHasNoID(t *testing.T) {
	c, s := pipePair(t)
	client := NewClient(c, nil)
	defer client.Close()
	if err := client.Notify("event", map[string]int{"x": 1}); err != nil {
		t.Fatal(err)
	}
	sc := bufio.NewScanner(s)
	if !sc.Scan() {
		t.Fatal("no frame")
	}
	var msg Message
	if err := json.Unmarshal(sc.Bytes(), &msg); err != nil {
		t.Fatal(err)
	}
	if len(msg.ID) != 0 {
		t.Errorf("a notification must not carry an id: %s", msg.ID)
	}
	if msg.Method != "event" {
		t.Errorf("method = %q", msg.Method)
	}
}

func TestCallTimeout(t *testing.T) {
	c, s := pipePair(t)
	defer s.Close()
	client := NewClient(c, nil)
	client.CallTimeout = 50 * time.Millisecond
	defer client.Close()
	start := time.Now()
	err := client.Call(context.Background(), "never", nil, nil)
	if err == nil {
		t.Fatal("expected a timeout")
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("timeout took too long: %s", time.Since(start))
	}
}

func TestContextCancellation(t *testing.T) {
	c, s := pipePair(t)
	defer s.Close()
	client := NewClient(c, nil)
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	if err := client.Call(ctx, "hang", nil, nil); err == nil {
		t.Fatal("expected a cancellation error")
	}
}

func TestMalformedFramesAreIgnored(t *testing.T) {
	c, s := pipePair(t)
	client := NewClient(c, nil)
	defer client.Close()
	if _, err := io.WriteString(s, "not json\n{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"ok\":true}}\n"); err != nil {
		t.Fatal(err)
	}
	var out struct {
		OK bool `json:"ok"`
	}
	if err := client.Call(context.Background(), "x", nil, &out); err != nil {
		t.Fatalf("the client should recover from a bad frame: %v", err)
	}
	if !out.OK {
		t.Error("valid frame after garbage was dropped")
	}
}

func TestConcurrentCalls(t *testing.T) {
	c, s := pipePair(t)
	var wg sync.WaitGroup
	go func() {
		sc := bufio.NewScanner(s)
		enc := json.NewEncoder(s)
		for sc.Scan() {
			var msg Message
			if err := json.Unmarshal(sc.Bytes(), &msg); err != nil {
				continue
			}
			_ = enc.Encode(Message{JSONRPC: "2.0", ID: msg.ID, Result: json.RawMessage(`{"ok":true}`)})
		}
	}()

	client := NewClient(c, nil)
	defer client.Close()
	var mu sync.Mutex
	errs := 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := client.Call(context.Background(), "ping", nil, nil); err != nil {
				mu.Lock()
				errs++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if errs != 0 {
		t.Errorf("%d concurrent calls failed", errs)
	}
	_ = s.Close()
}

func TestCallOnClosedConnectionFails(t *testing.T) {
	c, s := pipePair(t)
	client := NewClient(c, nil)
	client.Close()
	_ = s.Close()
	if err := client.Call(context.Background(), "x", nil, nil); err == nil {
		t.Error("expected an error after Close")
	}
}
