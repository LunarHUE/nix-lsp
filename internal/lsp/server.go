package lsp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
)

// errConnectionClosed is returned to a pending Call when the connection shuts
// down (read error, EOF, or context cancellation) before a response arrives.
var errConnectionClosed = errors.New("lsp: connection closed")

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

// Caller makes server-to-client JSON-RPC requests and waits for the response.
type Caller interface {
	Call(ctx context.Context, method string, params any, result any) error
}

// CallerHandler is implemented by handlers that need to make server-to-client
// requests.
type CallerHandler interface {
	SetCaller(Caller)
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

	callSeq       uint64
	pending       map[string]chan *Message
	pendingClosed bool
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
		pending:  make(map[string]chan *Message),
	}
	if notificationHandler, ok := handler.(NotificationHandler); ok {
		notificationHandler.SetNotifier(server)
	}
	if callerHandler, ok := handler.(CallerHandler); ok {
		callerHandler.SetCaller(server)
	}
	return server
}

// Call sends a server-to-client request and waits for its response. It returns
// the response error as a *ResponseError, ctx.Err() on cancellation, or an
// error if the connection closes before a response arrives.
func (s *Server) Call(ctx context.Context, method string, params any, result any) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	seq := atomic.AddUint64(&s.callSeq, 1)
	idRaw := json.RawMessage(fmt.Sprintf("%q", fmt.Sprintf("nixls-%d", seq)))
	key := requestKey(idRaw)
	ch := make(chan *Message, 1)

	s.mu.Lock()
	if s.pendingClosed {
		s.mu.Unlock()
		return errConnectionClosed
	}
	s.pending[key] = ch
	s.mu.Unlock()

	if err := s.writer.WriteMessage(newRequest(idRaw, method, params)); err != nil {
		s.mu.Lock()
		delete(s.pending, key)
		s.mu.Unlock()
		return err
	}

	select {
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.pending, key)
		s.mu.Unlock()
		return ctx.Err()
	case msg, ok := <-ch:
		if !ok || msg == nil {
			return errConnectionClosed
		}
		if msg.Error != nil {
			return msg.Error
		}
		if result != nil && len(msg.Result) > 0 {
			return json.Unmarshal(msg.Result, result)
		}
		return nil
	}
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
	defer s.closePending()

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
		s.routeResponse(msg)
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

// handleNotification never returns an error: JSON-RPC has no response channel
// for notifications, so a handler failure must not tear down the connection.
// Unknown methods (e.g. $/setTrace, which the spec says to ignore) are dropped
// silently; other handler errors are logged to stderr.
func (s *Server) handleNotification(ctx context.Context, method string, params json.RawMessage) error {
	if method == "initialized" || method == "exit" {
		return nil
	}
	_, err := s.handler.Handle(ctx, method, params)
	if err != nil {
		var rpcErr *ResponseError
		if !errors.As(err, &rpcErr) || rpcErr.Code != errMethodNotFound {
			fmt.Fprintf(os.Stderr, "lsp: notification %s: %v\n", method, err)
		}
	}
	return nil
}

func (s *Server) cancelRequest(params json.RawMessage) error {
	var decoded struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(params, &decoded); err != nil {
		// Notifications cannot be answered, so a malformed cancel is logged
		// and dropped rather than tearing down the connection.
		fmt.Fprintf(os.Stderr, "lsp: notification $/cancelRequest: decode params: %v\n", err)
		return nil
	}

	s.mu.Lock()
	cancel := s.requests[requestKey(decoded.ID)]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

// routeResponse delivers a client response to the matching pending Call. It
// runs synchronously on the read loop, so it never races closePending (which
// runs only after Run returns). Unknown IDs are silently dropped.
func (s *Server) routeResponse(msg *Message) {
	key := requestKey(msg.ID)
	s.mu.Lock()
	ch := s.pending[key]
	delete(s.pending, key)
	s.mu.Unlock()
	if ch == nil {
		return
	}
	ch <- msg
}

// closePending fails every in-flight Call when the connection shuts down.
// Closing each channel unblocks Call, which treats a closed channel as a
// connection-closed error. Setting pendingClosed makes later Calls fail fast.
func (s *Server) closePending() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pendingClosed = true
	for key, ch := range s.pending {
		close(ch)
		delete(s.pending, key)
	}
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
	TextDocumentSync          int                    `json:"textDocumentSync,omitempty"`
	DocumentSymbolProvider    bool                   `json:"documentSymbolProvider,omitempty"`
	DefinitionProvider        bool                   `json:"definitionProvider,omitempty"`
	HoverProvider             bool                   `json:"hoverProvider,omitempty"`
	DocumentHighlightProvider bool                   `json:"documentHighlightProvider,omitempty"`
	ReferencesProvider        bool                   `json:"referencesProvider,omitempty"`
	FoldingRangeProvider      bool                   `json:"foldingRangeProvider,omitempty"`
	WorkspaceSymbolProvider   bool                   `json:"workspaceSymbolProvider,omitempty"`
	CodeActionProvider        bool                   `json:"codeActionProvider,omitempty"`
	ExecuteCommandProvider    *ExecuteCommandOptions `json:"executeCommandProvider,omitempty"`
	CompletionProvider        *CompletionOptions     `json:"completionProvider,omitempty"`
}

// ExecuteCommandOptions advertises the workspace/executeCommand commands the
// server understands.
type ExecuteCommandOptions struct {
	Commands []string `json:"commands"`
}

// CompletionOptions advertises textDocument/completion support, the characters
// that trigger it, and whether the server resolves items lazily via
// completionItem/resolve (filling documentation only when an item is selected).
type CompletionOptions struct {
	TriggerCharacters []string `json:"triggerCharacters,omitempty"`
	ResolveProvider   bool     `json:"resolveProvider,omitempty"`
}

type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}
