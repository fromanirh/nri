/*
   Copyright The containerd Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package net

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/containerd/ttrpc"

	"os"
)

const (
	headerLength  = 8
	client uint32 = iota
	server
)

var (
	ErrClosed = ttrpc.ErrClosed
)

type Multiplexer interface {
	ClientConn() net.Conn
	ServerConn() net.Conn
	ServerWrite([]byte) (int, error)
	ClientWrite([]byte) (int, error)
	ServerRead([]byte) (int, error)
	ClientRead([]byte) (int, error)
	Close() error
}

type mux struct {
	conn  net.Conn
	wrch  chan *message
	cread chan []byte
	sread chan []byte

	closed    chan struct{}
	closeOnce sync.Once
	err       error
	errOnce   sync.Once
}

type message struct {
	kind uint32
	data []byte
	done chan error
}

type clientConn mux

type serverConn mux

// Multiplex returns a multiplexer for the given connection.
//
// Connection multiplexing is primarily meant to be used for
// multiplexing two ttRPC connections over a single connected
// socket. This allows both ends of the connection to act as a
// as a ttRPC client and as ttRPC servers registering services
// on the connection.
func Multiplex(conn net.Conn) *mux {
	m := &mux{
		conn:   conn,
		wrch:   make(chan *message),
		sread:  make(chan []byte, 32),
		cread:  make(chan []byte, 32),
		closed: make(chan struct{}),
	}

	go m.reader()
	go m.writer()

	return m
}

func (m *mux) ServerConn() net.Conn {
	return (*serverConn)(m)
}

func (sc *serverConn) Read(b []byte) (int, error) {
	return (*mux)(sc).ServerRead(b)
}

func (sc *serverConn) Write(b []byte) (int, error) {
	return (*mux)(sc).ServerWrite(b)
}

func (sc *serverConn) Close() error {
	return (*mux)(sc).Close()
}

func (sc *serverConn) LocalAddr() net.Addr {
	return (*mux)(sc).conn.LocalAddr()
}

func (sc *serverConn) RemoteAddr() net.Addr {
	return (*mux)(sc).conn.RemoteAddr()
}

func (sc *serverConn) SetDeadline(t time.Time) error {
	return (*mux)(sc).conn.SetDeadline(t)
}

func (sc *serverConn) SetReadDeadline(t time.Time) error {
	return (*mux)(sc).conn.SetReadDeadline(t)
}

func (sc *serverConn) SetWriteDeadline(t time.Time) error {
	return (*mux)(sc).conn.SetWriteDeadline(t)
}

func (m *mux) ClientConn() net.Conn {
	return (*clientConn)(m)
}

func (cc *clientConn) Read(b []byte) (int, error) {
	return (*mux)(cc).ClientRead(b)
}

func (cc *clientConn) Write(b []byte) (int, error) {
	return (*mux)(cc).ClientWrite(b)
}

func (cc *clientConn) Close() error {
	return (*mux)(cc).Close()
}

func (cc *clientConn) LocalAddr() net.Addr {
	return (*mux)(cc).conn.LocalAddr()
}

func (cc *clientConn) RemoteAddr() net.Addr {
	return (*mux)(cc).conn.RemoteAddr()
}

func (cc *clientConn) SetDeadline(t time.Time) error {
	return (*mux)(cc).conn.SetDeadline(t)
}

func (cc *clientConn) SetReadDeadline(t time.Time) error {
	return (*mux)(cc).conn.SetReadDeadline(t)
}

func (cc *clientConn) SetWriteDeadline(t time.Time) error {
	return (*mux)(cc).conn.SetWriteDeadline(t)
}

// Close closes the mux.
func (m *mux) Close() error {
	m.closeOnce.Do(func() {
		m.conn.Close()
		close(m.closed)
	})
	return nil
}

func (m *mux) error() error {
	m.errOnce.Do(func() {
		if m.err == nil {
			m.err = ErrClosed
		}
	})
	return m.err
}

func (m *mux) setError(err error) {
	m.errOnce.Do(func(){
		m.err = err
	})
}

// writer multiplexes messages to our peer.
//
// Each message is sent as a frame which consist of a
// the following fields:
//   - subchannel identifier (client or server)
//   - payload size
//   - actual payload (user-provided data)
//
// Any errors related to communication are returned to
// the caller. Once sending a message fails, all subsequent
// writes will fail with the same error until the mux is
// closed, after which point writes will fail with io.EOF.
func (m *mux) writer() {
	var (
		msg *message
		hdr [headerLength]byte
		err error
	)

	for {
		select {
		case <- m.closed:
			return
		case msg = <- m.wrch:
		}

		Debug("[writer] writing message '%s'...", string(msg.data))

		// once a write fails, all subsequent writes fail
		if err != nil {
			msg.done <- err
			continue
		}

		// write header (client/server, message size)
		binary.BigEndian.PutUint32(hdr[0:4], msg.kind)
		binary.BigEndian.PutUint32(hdr[4:8], uint32(len(msg.data)))

		Debug("[writer] %v: tag %d (%d), cnt %d (%d)", hdr[:],
			msg.kind, len(msg.data),
			binary.BigEndian.Uint32(hdr[0:4]),
			binary.BigEndian.Uint32(hdr[4:8]))

		_, err = m.conn.Write(hdr[:])
		if err != nil {
			msg.done <- err
			continue
		}

		// write payload
		_, err = m.conn.Write(msg.data)
		msg.done <- err
	}
}

// reader demultiplexes message from our peer
func (m *mux) reader() {
	var (
		hdr [headerLength]byte
		cnt uint32
		tag uint32
		buf []byte
		qch chan<- []byte
		err error
		got int
	)

	for {
		Debug("[reader-0] reading next message...")
		select {
		case <- m.closed:
			return
		default:
		}

		got, err = io.ReadFull(m.conn, hdr[:])
		if err != nil {
			Debug("[reader-1.5] closing due to error: %v", err)
			m.setError(err)
			close(m.sread)
			close(m.cread)
			return
		}

		Debug("[reader-1] read %d(,%v) bytes (%v)...", got, err, hdr[:])

		tag = binary.BigEndian.Uint32(hdr[0:4])
		cnt = binary.BigEndian.Uint32(hdr[4:8])
		buf = make([]byte, int(cnt))

		Debug("[reader-2] got %v cnt, tag %d, %d", hdr[:], cnt, tag)

		_, err = io.ReadFull(m.conn, buf)
		if err != nil {
			m.setError(err)
			close(m.sread)
			close(m.cread)
			return
		}

		Debug("[reader-3] got payload '%s'", string(buf))

		switch tag {
		case server:
			qch = m.sread
		case client:
			qch = m.cread
		default:
			m.setError(fmt.Errorf("invalid message tag %d", tag))
			close(m.sread)
			close(m.cread)
			return
		}

		select {
		case qch <- buf:
		default:
			// discard data
		}
	}
}

func (m *mux) ClientWrite(data []byte) (int, error) {
	return m.write(client, data)
}

func (m *mux) ServerWrite(data []byte) (int, error) {
	return m.write(server, data)
}

func (m *mux) ClientRead(data []byte) (n int, err error) {
	return m.read(client, data)
}

func (m *mux) ServerRead(data []byte) (n int, err error) {
	return m.read(server, data)
}

func (m *mux) write(kind uint32, data []byte) (int, error) {
	msg := &message{
		kind: kind,
		data: data,
		done: make(chan error),
	}

	select {
	case <- m.closed:
		return 0, m.error()
	case m.wrch <- msg:
	}

	err := <- msg.done
	if err != nil {
		return 0, err
	}

	return len(data), nil
}

func (m *mux) read(kind uint32, data []byte) (int, error) {
	var (
		buf []byte
		cnt int
		err error
		ok  bool
	)

	if kind == server {
		buf, ok = <- m.sread
	} else {
		buf, ok = <- m.cread
	}

	if !ok {
		Debug("[read]: %d read not ok...", kind)
		return 0, m.error()
	}

	cnt = copy(data, buf)

	if cnt < len(buf) {
		err = syscall.ENOMEM
	}

	return cnt, err
}

var debug bool

func Debug(format string, args ...interface{}) {
	if debug {
		fmt.Fprintf(os.Stdout, "D: "+format+"\n", args...)
	}
}

func Info(format string, args ...interface{}) {
	fmt.Fprintf(os.Stdout, "I: "+format+"\n", args...)
}

func Error(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "E: "+format+"\n", args...)
}

func Fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "fatal error: "+format+"\n", args...)
	os.Exit(1)
}

func init() {
	debug = os.Getenv("DEBUG") != ""
}
