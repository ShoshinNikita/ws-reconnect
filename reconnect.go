package reconnect

import (
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pkg/errors"
)

var (
	ErrNotDialed     = errors.New("method 'Dial' wasn't called")
	ErrAlreadyDialed = errors.New("method 'Dial' was called already")

	ErrNotConnected = errors.New("not connected")
)

type ReConn struct {
	mu sync.RWMutex

	conn              wsConnection
	errDialResp       *http.Response
	nextReconnectTime time.Time

	// read-only after 'Dial' call

	dialed           bool
	url              string
	handshakeTimeout time.Duration
	reconnectTimeout time.Duration
	subscribeHandler func() error
}

type wsConnection interface {
	ReadMessage() (messageType int, p []byte, err error)
	WriteMessage(messageType int, data []byte) error
	Close() error
}

type SubscribeHandler func() error

func New() *ReConn {
	return &ReConn{
		nextReconnectTime: time.Now(),
	}
}

// SetURL sets url. After 'Dial' call it does nothing
func (r *ReConn) SetURL(url string) *ReConn {
	if !r.dialed {
		r.url = url
	}
	return r
}

// SetHandshakeTimeout sets handshake timeout. After 'Dial' call it does nothing
func (r *ReConn) SetHandshakeTimeout(d time.Duration) *ReConn {
	if !r.dialed {
		r.handshakeTimeout = d
	}
	return r
}

// SetReconnectTimeout sets reconnect timeout. After 'Dial' call it does nothing
func (r *ReConn) SetReconnectTimeout(d time.Duration) *ReConn {
	if !r.dialed {
		r.reconnectTimeout = d
	}
	return r
}

// SetSubscribeHandler sets subscribe handler. After 'Dial' call it does nothing
func (r *ReConn) SetSubscribeHandler(f SubscribeHandler) *ReConn {
	if !r.dialed {
		r.subscribeHandler = f
	}
	return r
}

func (r *ReConn) Dial() error {
	if r.dialed {
		return ErrAlreadyDialed
	}
	r.dialed = true

	return r.connect()
}

func (r *ReConn) ReadMessage() (messageType int, data []byte, readErr error) {
	if !r.dialed {
		return 0, nil, ErrNotDialed
	}

	messageType, data, readErr = r.readMessage()
	if readErr == nil {
		return messageType, data, nil
	}

	// Try to reconnect
	if recErr := r.connect(); recErr != nil {
		return messageType, data, errors.Errorf("original error: '%s', reconnect error: '%s'", readErr, recErr)
	}

	return messageType, data, readErr
}

func (r *ReConn) readMessage() (messageType int, p []byte, err error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.conn == nil {
		return 0, nil, ErrNotConnected
	}

	return r.conn.ReadMessage()
}

func (r *ReConn) WriteMessage(messageType int, data []byte) error {
	if !r.dialed {
		return ErrNotDialed
	}

	writeErr := r.writeMessage(messageType, data)
	if writeErr == nil {
		return nil
	}

	// Try to reconnect
	if recErr := r.connect(); recErr != nil {
		return errors.Errorf("original error: '%s', reconnect error: '%s'", writeErr, recErr)
	}

	return writeErr
}

func (r *ReConn) writeMessage(messageType int, data []byte) error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if r.conn == nil {
		return ErrNotConnected
	}

	return r.conn.WriteMessage(messageType, data)
}

func (r *ReConn) connect() (err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	defer func() {
		if err == nil {
			return
		}
		r.nextReconnectTime = time.Now().Add(r.reconnectTimeout)
	}()

	if r.conn != nil {
		// Close previous connection
		r.conn.Close()
		r.conn = nil
	}

	<-time.After(r.nextReconnectTime.Sub(time.Now()))

	if r.conn, r.errDialResp, err = r.newDialer().Dial(r.url, nil); err != nil {
		return errors.Wrap(err, "dial error")
	}
	if r.subscribeHandler != nil {
		if err := r.subscribeHandler(); err != nil {
			r.conn.Close()
			return errors.Wrap(err, "subscribe error")
		}
	}

	return nil
}

func (r *ReConn) newDialer() *websocket.Dialer {
	return &websocket.Dialer{
		HandshakeTimeout: r.handshakeTimeout,
	}
}

func (r *ReConn) Close() error {
	if !r.dialed {
		return ErrNotDialed
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.conn == nil {
		return ErrNotConnected
	}

	err := r.conn.Close()
	r.conn = nil
	return err
}
