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
	"io"
	"net"
	"os"
	"strconv"

	"github.com/pkg/errors"
)

// NewFdConn creates a net.Conn for the given (socket) fd.
func NewFdConn(fd int) (net.Conn, error) {
	f := os.NewFile(uintptr(fd), "socket-fd#%d" + strconv.Itoa(fd))

	conn, err := net.FileConn(f)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create net.Conn for fd #%d", fd)
	}
	f.Close()

	return conn, nil
}

// connListener wraps a pre-connected socket in a net.Listener.
type connListener struct {
	next chan net.Conn
	conn net.Conn
	addr net.Addr
}

// NewConnListener wraps an existing net.Conn in a net.Listener.
//
// The first call to Accept() on the listener will return the wrapped
// connection. Subsequent calls to Accept() block until the listener
// is closed, then return io.EOF. Close() closes the listener and the
// wrapped connection.
func NewConnListener(conn net.Conn) *connListener {
	next := make(chan net.Conn, 1)
	next <- conn

	return &connListener{
		next: next,
		conn: conn,
		addr: conn.LocalAddr(),
	}
}

// Accept returns the wrapped connection when it is called the first
// time. Later calls to Accept block until the listener is closed, then
// return io.EOF.
func (l *connListener) Accept() (net.Conn, error) {
	conn := <-l.next
	if conn == nil {
		return nil, io.EOF
	}
	return conn, nil
}

// Close closes the listener and the wrapped connection.
func (l *connListener) Close() error {
	close(l.next)
	return l.conn.Close()
}

// Addr returns the local address of the wrapped connection.
func (l *connListener) Addr() net.Addr {
	return l.addr
}
