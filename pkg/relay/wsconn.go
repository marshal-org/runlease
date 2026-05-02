package relay

import (
	"io"
	"net"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// wsConn wraps a gorilla WebSocket connection as a net.Conn.
// Handshake JSON and raw bridged bytes both travel as binary frames.
// Gorilla's rule (one concurrent reader, one concurrent writer) is satisfied
// by the relay coordinator: it writes during handshake, then io.Copy takes
// over with exactly one reader goroutine and one writer goroutine per conn.
type wsConn struct {
	conn *websocket.Conn
	wmu  sync.Mutex // protects concurrent writes (e.g. writeMsg + bridge)
	r    io.Reader  // current frame reader; nil when between frames
}

// NewWSConn wraps ws as a net.Conn. Used by the CLI and agent to wrap a
// gorilla WebSocket dial result so the rest of the code sees net.Conn.
func NewWSConn(ws *websocket.Conn) net.Conn {
	return &wsConn{conn: ws}
}

func (c *wsConn) Read(p []byte) (int, error) {
	for {
		if c.r != nil {
			n, err := c.r.Read(p)
			if err == io.EOF {
				c.r = nil
				if n > 0 {
					return n, nil
				}
				continue
			}
			return n, err
		}
		_, r, err := c.conn.NextReader()
		if err != nil {
			return 0, err
		}
		c.r = r
	}
}

func (c *wsConn) Write(p []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if err := c.conn.WriteMessage(websocket.BinaryMessage, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (c *wsConn) Close() error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.conn.WriteMessage(websocket.CloseMessage, //nolint:errcheck
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	return c.conn.Close()
}

func (c *wsConn) LocalAddr() net.Addr  { return c.conn.LocalAddr() }
func (c *wsConn) RemoteAddr() net.Addr { return c.conn.RemoteAddr() }

func (c *wsConn) SetDeadline(t time.Time) error {
	if err := c.conn.SetReadDeadline(t); err != nil {
		return err
	}
	return c.conn.SetWriteDeadline(t)
}

func (c *wsConn) SetReadDeadline(t time.Time) error  { return c.conn.SetReadDeadline(t) }
func (c *wsConn) SetWriteDeadline(t time.Time) error { return c.conn.SetWriteDeadline(t) }
