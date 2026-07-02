package lsp

import (
	"encoding/json"
	"fmt"
)

const jsonrpcVersion = "2.0"

type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	HasID   bool            `json:"-"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *ResponseError  `json:"error,omitempty"`
}

func (m *Message) UnmarshalJSON(data []byte) error {
	type message Message
	var decoded message
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}

	*m = Message(decoded)
	_, m.HasID = fields["id"]
	return nil
}

func (m Message) IsRequest() bool {
	return m.Method != "" && m.HasID
}

func (m Message) IsNotification() bool {
	return m.Method != "" && !m.HasID
}

func (m Message) IsResponse() bool {
	return m.Method == "" && m.HasID
}

type ResponseError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *ResponseError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("json-rpc error %d: %s", e.Code, e.Message)
}

type responseMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *ResponseError  `json:"error,omitempty"`
}

type notificationMessage struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type requestMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  any             `json:"params,omitempty"`
}

func (m responseMessage) MarshalJSON() ([]byte, error) {
	if m.Error != nil {
		return json.Marshal(struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Error   *ResponseError  `json:"error"`
		}{
			JSONRPC: m.JSONRPC,
			ID:      m.ID,
			Error:   m.Error,
		})
	}

	return json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  any             `json:"result"`
	}{
		JSONRPC: m.JSONRPC,
		ID:      m.ID,
		Result:  m.Result,
	})
}

func newResultResponse(id json.RawMessage, result any) responseMessage {
	return responseMessage{JSONRPC: jsonrpcVersion, ID: id, Result: result}
}

func newErrorResponse(id json.RawMessage, code int, message string) responseMessage {
	return responseMessage{
		JSONRPC: jsonrpcVersion,
		ID:      id,
		Error:   &ResponseError{Code: code, Message: message},
	}
}

func newNotification(method string, params any) notificationMessage {
	return notificationMessage{JSONRPC: jsonrpcVersion, Method: method, Params: params}
}

func newRequest(id json.RawMessage, method string, params any) requestMessage {
	return requestMessage{JSONRPC: jsonrpcVersion, ID: id, Method: method, Params: params}
}

type CancelError struct {
	Message string
}

func (e *CancelError) Error() string {
	if e == nil || e.Message == "" {
		return "request canceled"
	}
	return e.Message
}
