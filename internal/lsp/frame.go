package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

const contentLengthHeader = "content-length"

// Reader reads LSP messages framed with Content-Length headers.
type Reader struct {
	r *bufio.Reader
}

func NewReader(r io.Reader) *Reader {
	return &Reader{r: bufio.NewReader(r)}
}

func (r *Reader) ReadMessage() (*Message, error) {
	body, err := r.ReadFrame()
	if err != nil {
		return nil, err
	}

	var msg Message
	if err := json.Unmarshal(body, &msg); err != nil {
		return nil, fmt.Errorf("decode json-rpc message: %w", err)
	}
	return &msg, nil
}

func (r *Reader) ReadFrame() ([]byte, error) {
	length, err := r.readContentLength()
	if err != nil {
		return nil, err
	}

	body := make([]byte, length)
	if _, err := io.ReadFull(r.r, body); err != nil {
		return nil, fmt.Errorf("read message body: %w", err)
	}
	return body, nil
}

func (r *Reader) readContentLength() (int, error) {
	length := -1
	for {
		line, err := r.r.ReadString('\n')
		if err != nil {
			if err == io.EOF && line == "" {
				return 0, io.EOF
			}
			return 0, fmt.Errorf("read header: %w", err)
		}

		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if length < 0 {
				return 0, fmt.Errorf("missing Content-Length header")
			}
			return length, nil
		}

		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return 0, fmt.Errorf("malformed header line %q", line)
		}
		if strings.ToLower(strings.TrimSpace(name)) != contentLengthHeader {
			continue
		}

		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || parsed < 0 {
			return 0, fmt.Errorf("invalid Content-Length %q", strings.TrimSpace(value))
		}
		length = parsed
	}
}

// Writer writes LSP messages framed with Content-Length headers.
type Writer struct {
	mu sync.Mutex
	w  io.Writer
}

func NewWriter(w io.Writer) *Writer {
	return &Writer{w: w}
}

func (w *Writer) WriteMessage(msg any) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("encode json-rpc message: %w", err)
	}
	return w.WriteFrame(body)
}

func (w *Writer) WriteFrame(body []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if _, err := fmt.Fprintf(w.w, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return fmt.Errorf("write header: %w", err)
	}
	if _, err := w.w.Write(body); err != nil {
		return fmt.Errorf("write body: %w", err)
	}
	return nil
}
