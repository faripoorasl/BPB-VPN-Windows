// The app-facing protocol: JSON lines over localhost TCP. Every connection authenticates with
// the token from the info file, then sends {"id":n,"method":"…","params":{…}} requests and
// receives {"id":n,"result":…} / {"id":n,"error":"…"} answers. State and scanner events are
// broadcast to every connected client as {"event":"…",…}, mirroring the Android EventChannels.
package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// client is one connected app (the Flutter UI, possibly twice: after a hot restart).
type client struct {
	conn   net.Conn
	send   sync.Mutex
	closed bool
}

func serveConn(conn net.Conn, token string, service *Service) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	enc := json.NewEncoder(conn)

	// First line: {"auth":"…"} — anything else ends the connection.
	var hello struct {
		Auth string `json:"auth"`
	}
	line, err := readJSONLine(reader, &hello)
	if err != nil || line == 0 {
		return
	}
	if hello.Auth != token {
		enc.Encode(map[string]any{"error": "unauthorized"})
		return
	}
	if err := enc.Encode(map[string]any{"result": map[string]any{"ok": true}}); err != nil {
		return
	}

	c := &client{conn: conn}
	service.addClient(c)
	defer service.removeClient(c)

	for {
		var req struct {
			ID     int             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := decodeJSONLine(reader, &req); err != nil {
			return
		}
		go func(id int, method string, params json.RawMessage) {
			result, errText := service.handle(method, params)
			response := map[string]any{"id": id}
			if errText != "" {
				response["error"] = errText
			} else {
				response["result"] = result
			}
			c.sendJSON(response)
		}(req.ID, req.Method, req.Params)
	}
}

// readJSONLine reads one JSON line; the returned count is 0 on immediate EOF.
func readJSONLine(reader *bufio.Reader, v any) (int, error) {
	line, err := reader.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		if err == io.EOF {
			return 0, nil
		}
		return 0, err
	}
	if err := json.Unmarshal(line, v); err != nil {
		return len(line), err
	}
	return len(line), nil
}

// decodeJSONLine is readJSONLine without the line length (requests always have a body).
func decodeJSONLine(reader *bufio.Reader, v any) error {
	_, err := readJSONLine(reader, v)
	return err
}

// sendJSON writes one line, serialized so answers never interleave.
func (c *client) sendJSON(v any) {
	c.send.Lock()
	defer c.send.Unlock()
	if c.closed {
		return
	}
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	c.conn.SetWriteDeadline(writeDeadline())
	if _, err := c.conn.Write(append(data, '\n')); err != nil {
		c.closed = true
	}
}

func writeDeadline() time.Time { return time.Now().Add(10 * time.Second) }

func randomToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("token: %w", err)
	}
	return hex.EncodeToString(b), nil
}
