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

package main

import (
	"fmt"
	stdnet "net"
	"os"
	"sync"
	"time"

	"github.com/containerd/nri/pkg/net"
	"github.com/containerd/nri/pkg/net/multiplex"
)

const (
	msgCount = 16 * 1024
	msgBurst = 32
)

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

func writer(wg *sync.WaitGroup, conn stdnet.Conn, which string) {
	kind := which + "-writer"
	for i := 0; i < msgCount; i++ {
		msg := fmt.Sprintf("[%s] this is message #%d", kind, i)
		_, err := conn.Write([]byte(msg))
		if err != nil {
			Error("[%s] failed to write message #%d: %v", kind, i, err)
			break
		}
		Info("[%s] wrote message #%d...", kind, i)

		if i > 0 && (i % msgBurst) == 0 {
			time.Sleep(25 * time.Millisecond)
		}
	}

	Info("[%s] done.", kind)
	wg.Done()
}

func reader(wg *sync.WaitGroup, conn stdnet.Conn, which string) {
	var (
		msg [256]byte
		idx int
	)

	kind := which + "-reader"

	for {
		Info("[%s] waiting for message #%d...", kind, idx)
		cnt, err := conn.Read(msg[:])
		if err != nil {
			Error("[%s] failed to read message #%d: %v", kind, idx, err)
			break
		}
		Info("[%s] read message #%d: %s", kind, idx, string(msg[:cnt]))
		idx++

		if idx == msgCount {
			break
		}
	}

	Info("[%s] done.", kind)
	wg.Done()
}

func main() {
	sockets, err := net.NewSocketPair()
	if err != nil {
		Fatal("failed to create test connection: %v", err)
	}

	sConn, err := sockets.LocalConn()
	if err != nil {
		Fatal("failed to create server side connection")
	}

	cConn, err := sockets.PeerConn()
	if err != nil {
		Fatal("failed to create client side connection")
	}

	sID := multiplex.ConnID(1)
	cID := multiplex.ConnID(2)

	sMux := multiplex.Multiplex(sConn)
	sWrite, err := sMux.Open(sID)
	if err != nil {
		Fatal("failed to open server connection for writing: %v", err)
	}
	sRead, err := sMux.Open(cID)
	if err != nil {
		Fatal("failed to open server connection for reading: %v", err)
	}

	cMux := multiplex.Multiplex(cConn)
	cWrite, err := cMux.Open(cID)
	if err != nil {
		Fatal("failed to open client connection for writing: %v", err)
	}
	cRead, err := cMux.Open(sID)
	if err != nil {
		Fatal("failed to open client connection for reading: %v", err)
	}


	wwg := &sync.WaitGroup{}
	rwg := &sync.WaitGroup{}

	wwg.Add(1)
	go writer(wwg, sWrite, "server")
	rwg.Add(1)
	go reader(rwg, sRead, "server")

	wwg.Add(1)
	go writer(wwg, cWrite, "client")
	rwg.Add(1)
	go reader(rwg, cRead, "client")

	wwg.Wait()
	rwg.Wait()
	sRead.Close()
	cRead.Close()
}
