package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"
)

// fakeClient drives one side of an io.Pipe pair as a minimal LSP peer: it reads
// framed messages the server writes and can write framed responses back.
type fakeClient struct {
	reader *Reader
	writer *Writer
}

func newPipeServer(t *testing.T, handler Handler) (*Server, *fakeClient, func()) {
	t.Helper()
	// serverIn is what the server reads (client writes into it); serverOut is
	// what the server writes (client reads from it).
	serverInR, serverInW := io.Pipe()
	serverOutR, serverOutW := io.Pipe()

	server := NewServer(serverInR, serverOutW, handler)
	client := &fakeClient{
		reader: NewReader(serverOutR),
		writer: NewWriter(serverInW),
	}
	cleanup := func() {
		serverInW.Close()
		serverInR.Close()
		serverOutW.Close()
		serverOutR.Close()
	}
	return server, client, cleanup
}

func TestServerCallHappyPath(t *testing.T) {
	server, client, cleanup := newPipeServer(t, HandlerFunc(func(context.Context, string, json.RawMessage) (any, error) {
		return nil, nil
	}))
	defer cleanup()

	runErr := make(chan error, 1)
	go func() { runErr <- server.Run(context.Background()) }()

	type result struct {
		OK bool `json:"ok"`
	}

	callDone := make(chan error, 1)
	var got result
	go func() {
		callDone <- server.Call(context.Background(), "window/workDoneProgress/create", map[string]any{"token": "t1"}, &got)
	}()

	// The client reads the request and replies with a result.
	msg, err := client.reader.ReadMessage()
	if err != nil {
		t.Fatalf("client ReadMessage error = %v", err)
	}
	if msg.Method != "window/workDoneProgress/create" || !msg.IsRequest() {
		t.Fatalf("got %+v, want a create request", msg)
	}
	var params struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(msg.Params, &params); err != nil {
		t.Fatalf("decode params: %v", err)
	}
	if params.Token != "t1" {
		t.Fatalf("token = %q, want t1", params.Token)
	}
	if err := client.writer.WriteMessage(responseMessage{JSONRPC: jsonrpcVersion, ID: msg.ID, Result: result{OK: true}}); err != nil {
		t.Fatalf("client write response: %v", err)
	}

	select {
	case err := <-callDone:
		if err != nil {
			t.Fatalf("Call error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Call did not return")
	}
	if !got.OK {
		t.Fatalf("result = %+v, want OK true", got)
	}
}

func TestServerCallErrorResponse(t *testing.T) {
	server, client, cleanup := newPipeServer(t, HandlerFunc(func(context.Context, string, json.RawMessage) (any, error) {
		return nil, nil
	}))
	defer cleanup()

	go func() { _ = server.Run(context.Background()) }()

	callDone := make(chan error, 1)
	go func() {
		callDone <- server.Call(context.Background(), "some/method", nil, nil)
	}()

	msg, err := client.reader.ReadMessage()
	if err != nil {
		t.Fatalf("client ReadMessage error = %v", err)
	}
	if err := client.writer.WriteMessage(responseMessage{
		JSONRPC: jsonrpcVersion,
		ID:      msg.ID,
		Error:   &ResponseError{Code: errInvalidRequest, Message: "nope"},
	}); err != nil {
		t.Fatalf("client write error response: %v", err)
	}

	select {
	case err := <-callDone:
		var rpcErr *ResponseError
		if !errors.As(err, &rpcErr) {
			t.Fatalf("Call error = %v (%T), want *ResponseError", err, err)
		}
		if rpcErr.Code != errInvalidRequest || rpcErr.Message != "nope" {
			t.Fatalf("ResponseError = %+v, want code %d message nope", rpcErr, errInvalidRequest)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Call did not return")
	}
}

func TestServerCallCanceledByContext(t *testing.T) {
	server, client, cleanup := newPipeServer(t, HandlerFunc(func(context.Context, string, json.RawMessage) (any, error) {
		return nil, nil
	}))
	defer cleanup()

	go func() { _ = server.Run(context.Background()) }()

	ctx, cancel := context.WithCancel(context.Background())
	callDone := make(chan error, 1)
	go func() {
		callDone <- server.Call(ctx, "some/method", nil, nil)
	}()

	// Drain the request so the write does not block, then cancel without replying.
	if _, err := client.reader.ReadMessage(); err != nil {
		t.Fatalf("client ReadMessage error = %v", err)
	}
	cancel()

	select {
	case err := <-callDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Call error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Call did not return after cancel")
	}
}

func TestServerCallFailsWhenConnectionCloses(t *testing.T) {
	serverInR, serverInW := io.Pipe()
	serverOutR, serverOutW := io.Pipe()
	server := NewServer(serverInR, serverOutW, HandlerFunc(func(context.Context, string, json.RawMessage) (any, error) {
		return nil, nil
	}))
	client := &fakeClient{reader: NewReader(serverOutR), writer: NewWriter(serverInW)}

	runDone := make(chan error, 1)
	go func() { runDone <- server.Run(context.Background()) }()

	callDone := make(chan error, 1)
	go func() {
		callDone <- server.Call(context.Background(), "some/method", nil, nil)
	}()

	// Let the request land, then close the client's write end so the server's
	// reader hits EOF and Run returns, failing the pending Call.
	if _, err := client.reader.ReadMessage(); err != nil {
		t.Fatalf("client ReadMessage error = %v", err)
	}
	serverInW.Close()

	select {
	case err := <-callDone:
		if err == nil {
			t.Fatal("Call error = nil, want connection-closed error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Call did not return after connection close")
	}

	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after EOF")
	}
	serverOutW.Close()
	serverOutR.Close()
}
