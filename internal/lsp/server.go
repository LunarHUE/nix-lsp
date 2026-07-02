package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

const (
	errParseError     = -32700
	errInvalidRequest = -32600
	errMethodNotFound = -32601
	errInternalError  = -32603
	errRequestFailed  = -32803
	errRequestCancel  = -32800
)

type Handler interface {
	Handle(ctx context.Context, method string, params json.RawMessage) (any, error)
}

// Notifier sends server-to-client JSON-RPC notifications.
type Notifier interface {
	Notify(ctx context.Context, method string, params any) error
}

// NotificationHandler is implemented by handlers that need to publish
// server-to-client notifications.
type NotificationHandler interface {
	SetNotifier(Notifier)
}

type HandlerFunc func(ctx context.Context, method string, params json.RawMessage) (any, error)

func (f HandlerFunc) Handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	return f(ctx, method, params)
}

type Server struct {
	reader  *Reader
	writer  *Writer
	handler Handler

	mu           sync.Mutex
	shuttingDown bool
	requests     map[string]context.CancelFunc
	inflight     sync.WaitGroup
}

func NewServer(in io.Reader, out io.Writer, handler Handler) *Server {
	if handler == nil {
		handler = LifecycleHandler{}
	}
	server := &Server{
		reader:   NewReader(in),
		writer:   NewWriter(out),
		handler:  handler,
		requests: make(map[string]context.CancelFunc),
	}
	if notificationHandler, ok := handler.(NotificationHandler); ok {
		notificationHandler.SetNotifier(server)
	}
	return server
}

// Notify sends a server-to-client notification.
func (s *Server) Notify(ctx context.Context, method string, params any) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return s.writer.WriteMessage(newNotification(method, params))
}

func (s *Server) Run(ctx context.Context) error {
	defer s.inflight.Wait()

	for {
		select {
		case <-ctx.Done():
			s.cancelAll()
			return ctx.Err()
		default:
		}

		msg, err := s.reader.ReadMessage()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			s.cancelAll()
			return err
		}

		if err := s.dispatch(ctx, msg); err != nil {
			return err
		}
		if msg.IsNotification() && msg.Method == "exit" {
			return nil
		}
	}
}

func (s *Server) dispatch(ctx context.Context, msg *Message) error {
	switch {
	case msg.IsRequest():
		reqCtx, cancel := context.WithCancel(ctx)
		key := requestKey(msg.ID)
		s.mu.Lock()
		s.requests[key] = cancel
		s.mu.Unlock()

		s.inflight.Add(1)
		go s.dispatchRequest(reqCtx, msg, key, cancel)
		return nil
	case msg.IsNotification():
		if msg.Method == "$/cancelRequest" {
			return s.cancelRequest(msg.Params)
		}
		return s.handleNotification(ctx, msg.Method, msg.Params)
	case msg.IsResponse():
		return nil
	default:
		if msg.HasID {
			return s.writer.WriteMessage(newErrorResponse(msg.ID, errInvalidRequest, "invalid request"))
		}
		return nil
	}
}

func (s *Server) dispatchRequest(ctx context.Context, msg *Message, key string, cancel context.CancelFunc) {
	defer s.inflight.Done()

	defer func() {
		s.mu.Lock()
		delete(s.requests, key)
		s.mu.Unlock()
		cancel()
	}()

	result, err := s.handleRequest(ctx, msg.Method, msg.Params)
	if err != nil {
		_ = s.writer.WriteMessage(errorResponseFor(msg.ID, err))
		return
	}
	_ = s.writer.WriteMessage(newResultResponse(msg.ID, result))
}

func (s *Server) handleRequest(ctx context.Context, method string, params json.RawMessage) (any, error) {
	if method == "shutdown" {
		s.mu.Lock()
		s.shuttingDown = true
		s.mu.Unlock()
		return nil, nil
	}
	return s.handler.Handle(ctx, method, params)
}

func (s *Server) handleNotification(ctx context.Context, method string, params json.RawMessage) error {
	if method == "initialized" || method == "exit" {
		return nil
	}
	_, err := s.handler.Handle(ctx, method, params)
	return err
}

func (s *Server) cancelRequest(params json.RawMessage) error {
	var decoded struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(params, &decoded); err != nil {
		return fmt.Errorf("decode cancel params: %w", err)
	}

	s.mu.Lock()
	cancel := s.requests[requestKey(decoded.ID)]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

func (s *Server) cancelAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cancel := range s.requests {
		cancel()
	}
}

func requestKey(id json.RawMessage) string {
	return string(id)
}

func errorResponseFor(id json.RawMessage, err error) responseMessage {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return newErrorResponse(id, errRequestCancel, "request canceled")
	}

	var cancelErr *CancelError
	if errors.As(err, &cancelErr) {
		return newErrorResponse(id, errRequestCancel, cancelErr.Error())
	}

	var rpcErr *ResponseError
	if errors.As(err, &rpcErr) {
		return responseMessage{JSONRPC: jsonrpcVersion, ID: id, Error: rpcErr}
	}

	return newErrorResponse(id, errRequestFailed, err.Error())
}

type LifecycleHandler struct{}

func (LifecycleHandler) Handle(_ context.Context, method string, _ json.RawMessage) (any, error) {
	switch method {
	case "initialize":
		return InitializeResult{
			Capabilities: ServerCapabilities{
				TextDocumentSync: 1,
			},
			ServerInfo: &ServerInfo{
				Name: "nix-lsp",
			},
		}, nil
	default:
		return nil, &ResponseError{Code: errMethodNotFound, Message: fmt.Sprintf("method not found: %s", method)}
	}
}

type InitializeResult struct {
	Capabilities ServerCapabilities `json:"capabilities"`
	ServerInfo   *ServerInfo        `json:"serverInfo,omitempty"`
}

type ServerCapabilities struct {
	TextDocumentSync          int  `json:"textDocumentSync,omitempty"`
	DocumentSymbolProvider    bool `json:"documentSymbolProvider,omitempty"`
	DefinitionProvider        bool `json:"definitionProvider,omitempty"`
	DocumentHighlightProvider bool `json:"documentHighlightProvider,omitempty"`
	ReferencesProvider        bool `json:"referencesProvider,omitempty"`
	FoldingRangeProvider      bool `json:"foldingRangeProvider,omitempty"`
}

type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}
