package web

import (
	"encoding/json"
	"sync"
	"time"

	"golang.org/x/net/websocket"
)

// wsConn serialises writes on a WebSocket (several goroutines send) and puts
// a deadline on each, so a stalled browser cannot pin a host stream forever.
type wsConn struct {
	ws *websocket.Conn
	mu sync.Mutex
}

const wsWriteTimeout = 30 * time.Second

func (c *wsConn) send(v any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ws.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
	return websocket.Message.Send(c.ws, v) // string: text frame, []byte: binary frame
}

func (c *wsConn) sendJSON(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return c.send(string(b))
}

// recvText reads the next text message.
func (c *wsConn) recvText() (string, error) {
	var msg string
	err := websocket.Message.Receive(c.ws, &msg)
	return msg, err
}
