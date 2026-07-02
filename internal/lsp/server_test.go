package lsp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"testing"
)

func TestServerInitializeShutdownExitFlow(t *testing.T) {
	input := strings.Join([]string{
		rawFrame(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"processId":123}}`),
		rawFrame(`{"jsonrpc":"2.0","method":"initialized","params":{}}`),
		rawFrame(`{"jsonrpc":"2.0","id":2,"method":"shutdown"}`),
		rawFrame(`{"jsonrpc":"2.0","method":"exit"}`),
	}, "")

	var out bytes.Buffer
	err := NewServer(strings.NewReader(input), &out, LifecycleHandler{}).Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	responses := readAllMessages(t, &out)
	if len(responses) != 2 {
		t.Fatalf("got %d responses, want 2", len(responses))
	}
	byID := responsesByID(responses)
	initialize := byID["1"]
	if initialize == nil {
		t.Fatalf("missing initialize response: %#v", responses)
	}
	var init InitializeResult
	if err := json.Unmarshal(initialize.Result, &init); err != nil {
		t.Fatalf("decode initialize result: %v", err)
	}
	if init.Capabilities.TextDocumentSync != 1 {
		t.Fatalf("TextDocumentSync = %d, want 1", init.Capabilities.TextDocumentSync)
	}

	shutdown := byID["2"]
	if shutdown == nil {
		t.Fatalf("missing shutdown response: %#v", responses)
	}
	if shutdown.Error != nil {
		t.Fatalf("shutdown error = %v", shutdown.Error)
	}
	if string(shutdown.Result) != "null" {
		t.Fatalf("shutdown result = %s, want null", shutdown.Result)
	}
}

func TestServerUsesCanceledContextForHandlerResponse(t *testing.T) {
	input := rawFrame(`{"jsonrpc":"2.0","id":1,"method":"custom/test"}`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var out bytes.Buffer
	err := NewServer(strings.NewReader(input), &out, HandlerFunc(func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
		return nil, ctx.Err()
	})).Run(ctx)
	if err != context.Canceled {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
}

func TestServerMapsHandlerCancellationToJSONRPCError(t *testing.T) {
	input := rawFrame(`{"jsonrpc":"2.0","id":1,"method":"custom/test"}`)

	var out bytes.Buffer
	err := NewServer(strings.NewReader(input), &out, HandlerFunc(func(context.Context, string, json.RawMessage) (any, error) {
		return nil, context.Canceled
	})).Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	responses := readAllMessages(t, &out)
	if len(responses) != 1 {
		t.Fatalf("got %d responses, want 1", len(responses))
	}
	if responses[0].Error == nil {
		t.Fatalf("expected error response")
	}
	if responses[0].Error.Code != errRequestCancel {
		t.Fatalf("error code = %d, want %d", responses[0].Error.Code, errRequestCancel)
	}
}

func TestServerCancelRequestCancelsInFlightHandler(t *testing.T) {
	input := strings.Join([]string{
		rawFrame(`{"jsonrpc":"2.0","id":1,"method":"custom/slow"}`),
		rawFrame(`{"jsonrpc":"2.0","method":"$/cancelRequest","params":{"id":1}}`),
		rawFrame(`{"jsonrpc":"2.0","method":"exit"}`),
	}, "")

	var out bytes.Buffer
	err := NewServer(strings.NewReader(input), &out, HandlerFunc(func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})).Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	responses := readAllMessages(t, &out)
	if len(responses) != 1 {
		t.Fatalf("got %d responses, want 1", len(responses))
	}
	if responses[0].Error == nil {
		t.Fatal("expected cancellation error response")
	}
	if responses[0].Error.Code != errRequestCancel {
		t.Fatalf("error code = %d, want %d", responses[0].Error.Code, errRequestCancel)
	}
}

func TestServerCapabilitiesSerializeFeatureProviders(t *testing.T) {
	caps := ServerCapabilities{
		TextDocumentSync:          1,
		DocumentSymbolProvider:    true,
		DefinitionProvider:        true,
		DocumentHighlightProvider: true,
	}
	data, err := json.Marshal(caps)
	if err != nil {
		t.Fatalf("Marshal error = %v", err)
	}
	for _, field := range []string{
		`"documentSymbolProvider":true`,
		`"definitionProvider":true`,
		`"documentHighlightProvider":true`,
	} {
		if !strings.Contains(string(data), field) {
			t.Errorf("capabilities JSON %s missing %s", data, field)
		}
	}

	var round ServerCapabilities
	if err := json.Unmarshal(data, &round); err != nil {
		t.Fatalf("Unmarshal error = %v", err)
	}
	if round != caps {
		t.Fatalf("round-trip = %+v, want %+v", round, caps)
	}

	// Zero-value providers are omitted so a minimal handler advertises nothing
	// extra.
	empty, err := json.Marshal(ServerCapabilities{TextDocumentSync: 1})
	if err != nil {
		t.Fatalf("Marshal empty error = %v", err)
	}
	if strings.Contains(string(empty), "Provider") {
		t.Fatalf("empty capabilities JSON = %s, want no providers", empty)
	}
}

func readAllMessages(t *testing.T, r io.Reader) []*Message {
	t.Helper()
	reader := NewReader(r)
	var messages []*Message
	for {
		msg, err := reader.ReadMessage()
		if err == io.EOF {
			return messages
		}
		if err != nil {
			t.Fatalf("ReadMessage() error = %v", err)
		}
		messages = append(messages, msg)
	}
}

func responsesByID(messages []*Message) map[string]*Message {
	byID := make(map[string]*Message, len(messages))
	for _, msg := range messages {
		byID[string(msg.ID)] = msg
	}
	return byID
}

func rawFrame(body string) string {
	return "Content-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body
}
