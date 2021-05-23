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

package vproto

import (
	"context"
	stdnet "net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/pkg/errors"

	api "github.com/containerd/nri/api/plugin/vproto"
	"github.com/containerd/nri/pkg/net"
	"github.com/containerd/nri/pkg/net/multiplex"
	"github.com/containerd/ttrpc"
)

// startStaticPlugin starts a static plugin.
//
// The ttRPC connection to the plugin is run over a connected
// pair of sockets. Standard input is set up with a pipe. The
// plugin can monitor this pipe using a blocking read to shut
// itself down if the runtime exits unexpectedly (IOW without
// explicitly shutting down plugins first).
func startStaticPlugin(dir, name, conf string) (p *plugin, retErr error) {
	sockets, err := net.NewSocketPair()
	if err != nil {
		return nil, errors.Wrap(err, "failed to create plugin connection")
	}
	defer sockets.Close()

	conn, err := sockets.LocalConn()
	if err != nil {
		return nil, errors.Wrap(err, "failed to set up local connection")
	}

	peerFile := sockets.PeerFile()
	defer func() {
		peerFile.Close()
		if retErr != nil {
			conn.Close()
		}
	}()

	cmd := exec.Command(filepath.Join(dir, name))
	cmd.ExtraFiles = []*os.File{peerFile}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, errors.Wrap(err, "failed to create stdin pipe")
	}

	err = cmd.Start()
	if err != nil {
		return nil, errors.Wrap(err, "failed to start plugin")
	}

	mux := multiplex.Multiplex(conn)
	pconn, err := mux.Open(multiplex.PluginServiceConn)
	if err != nil {
		return nil, errors.Wrap(err, "failed to open mux plugin connection")
	}

	ttrpcc := ttrpc.NewClient(pconn)
	client := api.NewPluginClient(ttrpcc)
	p = &plugin{
		name:   name,
		cmd:    cmd,
		stdin:  stdin,
		conn:   conn,
		mux:    mux,
		ttrpcc: ttrpcc,
		client: client,
	}
	p.server, err = newServer(p)
	if err != nil {
		p.close()
		p.stop()
		log.Errorf("failed to create ttrpc server: %v", err)
	}

	err = p.configure(context.Background(), conf)
	if err != nil {
		p.close()
		p.stop()
		return nil, err
	}

	return p, nil
}

// newDynamicPlugin creates a new dynamic plugin for the connection.
func newDynamicPlugin(conn stdnet.Conn) (*plugin, error) {
	mux := multiplex.Multiplex(conn)

	rconn, err := mux.Open(multiplex.PluginServiceConn)
	if err != nil {
		mux.Close()
		return nil, errors.Wrap(err, "failed to open mux plugin connection")
	}

	ttrpcc := ttrpc.NewClient(rconn)

	client := api.NewPluginClient(ttrpcc)
	p := &plugin{
		conn:   conn,
		mux:    mux,
		ttrpcc: ttrpcc,
		client: client,
	}

	s, err := newServer(p)
	if err != nil {
		log.Errorf("failed to create plugin ttrpc server: %v", err)
	}
	p.server = s

	ctx := context.Background()
	err = p.configure(ctx, "")
	if err != nil {
		p.shutdown(ctx)
		p.close()
		return nil, err
	}

	return p, nil
}

// newSocketPair returns a socketpair for a plugin ttRPC transport.
func newSocketPair() (stdnet.Conn, *os.File, error) {
	fds, err := syscall.Socketpair(syscall.AF_LOCAL, syscall.SOCK_STREAM, 0)
	if err != nil {
		return nil, nil, err
	}

	fd1 := os.NewFile(uintptr(fds[0]), "ttrpc-socketpair[0]")
	defer fd1.Close()
	fd2 := os.NewFile(uintptr(fds[1]), "ttrpc-socketpair[1]")

	conn, err := stdnet.FileConn(fd1)
	if err != nil {
		fd2.Close()
		return nil, nil, errors.Wrap(err, "failed to conn-wrap socketpair[0]")
	}

	return conn, fd2, nil
}

// getPeerPid returns the process id of the connections peer.
func getPeerPid(conn stdnet.Conn) (int, error) {
	var cred *unix.Ucred

	uc, ok := conn.(*stdnet.UnixConn)
	if !ok {
		return 0, errors.Errorf("invalid connection, not *net.UnixConn")
	}

	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, errors.Wrap(err, "failed get raw unix domain connection")
	}

	ctrlErr := raw.Control(func(fd uintptr) {
		cred, err = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	})
	if err != nil {
		return 0, errors.Wrap(err, "failed to get process credentials")
	}
	if ctrlErr != nil {
		return 0, errors.Wrap(ctrlErr, "uc.SyscallConn().Control() failed")
	}

	return int(cred.Pid), nil
}
