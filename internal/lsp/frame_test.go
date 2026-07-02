package lsp

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"testing"
)

func TestReadFrameParsesContentLength(t *testing.T) {
	body := `{"jsonrpc":"2.0","method":"initialized"}`
	input := "Content-Type: application/vscode-jsonrpc; charset=utf-8\r\nContent-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body

	got, err := NewReader(strings.NewReader(input)).ReadFrame()
	if err != nil {
		t.Fatalf("ReadFrame() error = %v", err)
	}
	if string(got) != body {
		t.Fatalf("ReadFrame() = %q, want %q", got, body)
	}
}

func TestReadMessageClassifiesMessages(t *testing.T) {
	cases := []struct {
		name         string
		body         string
		wantRequest  bool
		wantNotify   bool
		wantResponse bool
	}{
		{name: "request", body: `{"jsonrpc":"2.0","id":1,"method":"initialize"}`, wantRequest: true},
		{name: "notification", body: `{"jsonrpc":"2.0","method":"initialized"}`, wantNotify: true},
		{name: "response", body: `{"jsonrpc":"2.0","id":1,"result":null}`, wantResponse: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg, err := NewReader(frame(tc.body)).ReadMessage()
			if err != nil {
				t.Fatalf("ReadMessage() error = %v", err)
			}
			if msg.IsRequest() != tc.wantRequest {
				t.Fatalf("IsRequest() = %v, want %v", msg.IsRequest(), tc.wantRequest)
			}
			if msg.IsNotification() != tc.wantNotify {
				t.Fatalf("IsNotification() = %v, want %v", msg.IsNotification(), tc.wantNotify)
			}
			if msg.IsResponse() != tc.wantResponse {
				t.Fatalf("IsResponse() = %v, want %v", msg.IsResponse(), tc.wantResponse)
			}
		})
	}
}

func TestWriteFrameIncludesContentLength(t *testing.T) {
	var out bytes.Buffer
	body := []byte(`{"jsonrpc":"2.0","result":null}`)

	if err := NewWriter(&out).WriteFrame(body); err != nil {
		t.Fatalf("WriteFrame() error = %v", err)
	}

	want := "Content-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + string(body)
	if out.String() != want {
		t.Fatalf("WriteFrame() = %q, want %q", out.String(), want)
	}
}

func TestWriteMessageRoundTrips(t *testing.T) {
	var out bytes.Buffer
	msg := newResultResponse(json.RawMessage(`"abc"`), map[string]string{"ok": "yes"})

	if err := NewWriter(&out).WriteMessage(msg); err != nil {
		t.Fatalf("WriteMessage() error = %v", err)
	}

	got, err := NewReader(&out).ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage() error = %v", err)
	}
	if !got.IsResponse() {
		t.Fatalf("message was not classified as response: %#v", got)
	}
	if string(got.ID) != `"abc"` {
		t.Fatalf("ID = %s, want %s", got.ID, `"abc"`)
	}
}

func TestReadFrameEOF(t *testing.T) {
	_, err := NewReader(strings.NewReader("")).ReadFrame()
	if err != io.EOF {
		t.Fatalf("ReadFrame() error = %v, want EOF", err)
	}
}

func frame(body string) io.Reader {
	return strings.NewReader("Content-Length: " + strconv.Itoa(len(body)) + "\r\n\r\n" + body)
}
