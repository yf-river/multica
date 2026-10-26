package agent

import (
	"bytes"
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// 原生 Unix 入口承载 WebSocket；每轮连接都有独立的 RPC ID 与事件流。
type codexResidentStream struct {
	conn    *websocket.Conn
	reader  *io.PipeReader
	writer  *io.PipeWriter
	done    chan struct{}
	once    sync.Once
	closing atomic.Bool
}

func openCodexResidentStream(ctx context.Context, socket string) (*codexResidentStream, error) {
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second, NetDialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	conn, response, err := dialer.DialContext(ctx, "ws://localhost/", nil)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(32 << 20)
	reader, writer := io.Pipe()
	stream := &codexResidentStream{conn: conn, reader: reader, writer: writer, done: make(chan struct{})}
	go func() {
		defer close(stream.done)
		defer writer.Close()
		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				if !stream.closing.Load() {
					_ = writer.CloseWithError(err)
				}
				return
			}
			if _, err := writer.Write(append(message, '\n')); err != nil {
				return
			}
		}
	}()
	return stream, nil
}

func (s *codexResidentStream) Write(data []byte) (int, error) {
	_ = s.conn.SetWriteDeadline(time.Now().Add(8 * time.Second))
	if err := s.conn.WriteMessage(websocket.TextMessage, bytes.TrimSuffix(data, []byte{'\n'})); err != nil {
		return 0, err
	}
	return len(data), nil
}

func (s *codexResidentStream) Close() error {
	s.once.Do(func() {
		s.closing.Store(true)
		_ = s.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
		_ = s.conn.Close()
		_ = s.reader.Close()
		_ = s.writer.Close()
	})
	return nil
}
