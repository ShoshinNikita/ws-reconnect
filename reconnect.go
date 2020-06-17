package reconnect

import (
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var (
	ErrNotDialed     = errors.New("method 'Dial' wasn't called")
	ErrAlreadyDialed = errors.New("method 'Dial' was already called")

	ErrNotConnected = errors.New("not connected")
	ErrConnClosed   = errors.New("closed")

	// ErrDial is used when 'websocket.Dial' returns an error
	ErrDial = errors.New("dial error")
	// ErrSubscribe is used when subscribe handler returns an error
	ErrSubscribe = errors.New("subscribe error")
	// ErrReconnect is used when reconnection wasn't successful
	ErrReconnect = errors.New("reconnect error")
)

type ReConn struct {
	mu  sync.RWMutex
	log Logger

	conn              WsConnection
	dialBody          []byte
	nextReconnectTime time.Time

	// read-only after 'Dial' call

	dialed           bool
	url              string
	handshakeTimeout time.Duration
	reconnectTimeout time.Duration
	subscribeHandler SubscribeHandler
}

type WsConnection interface {
	ReadMessage() (messageType int, p []byte, err error)
	WriteMessage(messageType int, data []byte) error
	Close() error
}

type Logger interface {
	Debug(msg string)
	Info(msg string)
	Error(msg string)
}

type SubscribeHandler func(WsConnection) error

// New creates a new instance of 'ReConn'. To set url, timeouts and etc. use methods 'Set...'
func New() *ReConn {
	return &ReConn{
		log: NoopLogger{},
		//
		nextReconnectTime: time.Now(),
	}
}

// ----------------------------------------------------
// Setters
// ----------------------------------------------------

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

// SetLogger sets logger. After 'Dial' call it does nothing
func (r *ReConn) SetLogger(log Logger) *ReConn {
	if !r.dialed {
		if log == nil {
			log = NoopLogger{}
		}
		r.log = log
	}
	return r
}

func (r *ReConn) Dial() error {
	if r.dialed {
		return ErrAlreadyDialed
	}
	r.dialed = true

	return r.connect(true)
}

// ----------------------------------------------------
// Read/Write methods
// ----------------------------------------------------

func (r *ReConn) ReadMessage() (messageType int, data []byte, readErr error) {
	if !r.dialed {
		return 0, nil, ErrNotDialed
	}

	messageType, data, readErr = r.readMessage()
	if readErr == nil {
		return messageType, data, nil
	}

	// Try to reconnect
	if recErr := r.connect(false); recErr != nil {
		if recErr == ErrConnClosed {
			return messageType, data, readErr
		}

		return messageType, data, fmt.Errorf("%w: original error: '%s', reconnect error: '%s'", ErrReconnect, readErr, recErr)
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
	if recErr := r.connect(false); recErr != nil {
		if recErr == ErrConnClosed {
			return writeErr
		}

		return fmt.Errorf("%w: original error: '%s', reconnect error: '%s'", ErrReconnect, writeErr, recErr)
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

func (r *ReConn) connect(firstTime bool) (err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !firstTime && r.conn == nil {
		// Connection was closed
		return ErrConnClosed
	}

	defer func() {
		if err == nil {
			return
		}
		r.nextReconnectTime = time.Now().Add(r.reconnectTimeout)
	}()

	if r.conn != nil {
		r.log.Debug("close previous connection")
		// Close previous connection
		r.conn.Close()
		r.conn = nil
	}

	<-time.After(time.Until(r.nextReconnectTime))

	r.log.Info(fmt.Sprintf("connect to '%s'", r.url))

	var resp *http.Response
	r.conn, resp, err = r.newDialer().Dial(r.url, nil)
	if resp != nil && resp.Body != nil {
		// Save response body
		r.dialBody, _ = ioutil.ReadAll(resp.Body)
		resp.Body.Close()
	}
	if err != nil {
		err = fmt.Errorf("%w: %s", ErrDial, err)
		r.log.Error(err.Error())
		return err
	}

	if r.subscribeHandler != nil {
		r.log.Debug("call subscribe handler")

		// Pass raw connection to prevent deadlock
		if err := r.subscribeHandler(r.conn); err != nil {
			err = fmt.Errorf("%w: %s", ErrSubscribe, err)
			r.log.Error(err.Error())

			r.conn.Close()
			return err
		}
	}

	return nil
}

func (r *ReConn) newDialer() *websocket.Dialer {
	return &websocket.Dialer{
		HandshakeTimeout: r.handshakeTimeout,
	}
}

// Close closes connection
func (r *ReConn) Close() error {
	if !r.dialed {
		return ErrNotDialed
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if r.conn == nil {
		return ErrNotConnected
	}

	r.log.Debug("close connection")

	conn := r.conn
	r.conn = nil
	return conn.Close()
}

func (r *ReConn) GetDialBody() []byte {
	r.mu.RLock()
	defer r.mu.RUnlock()

	bodyCopy := make([]byte, len(r.dialBody))
	copy(bodyCopy, r.dialBody)
	return bodyCopy
}

// ----------------------------------------------------
// Noop logger
// ----------------------------------------------------

type NoopLogger struct{}

var _ Logger = (*NoopLogger)(nil)

func (NoopLogger) Debug(msg string) {}
func (NoopLogger) Info(msg string)  {}
func (NoopLogger) Error(msg string) {}
