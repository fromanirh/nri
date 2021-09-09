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

package v2alpha1

import (
	"context"
	stdnet "net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sys/unix"
	"github.com/pkg/errors"

	api "github.com/containerd/nri/pkg/api/plugin/v2alpha1"
	"github.com/containerd/nri/pkg/log"
	"github.com/containerd/nri/pkg/net"
	"github.com/containerd/nri/pkg/net/multiplex"
	"github.com/containerd/ttrpc"
)

const (
	runPodSandbox int = (1 << iota)
	stopPodSandbox
	removePodSandbox
	createContainer
	postCreateContainer
	startContainer
	postStartContainer
	updateContainer
	postUpdateContainer
	stopContainer
	removeContainer
	allEvents = 0x7fffffff

	pluginRegisterTimeout = 1 * time.Second
	pluginRequestTimeout  = 3 * time.Second
	pluginShutdownTimeout = 3 * time.Second
)

type plugin struct {
	lock   sync.Mutex
	name   string
	cfg    string
	id     string
	pid    int
	cmd    *exec.Cmd
	mux    multiplex.Mux
	rpcc   *ttrpc.Client
	rpcl   stdnet.Listener
	rpcs   *ttrpc.Server
	events int
	closed bool
	stub   api.PluginService
	regC   chan error
	closeC chan struct{}
	r      *Runtime
}

func newLaunchedPlugin(dir, name, cfg string) (p *plugin, retErr error) {
	sockets, err := net.NewSocketPair()
	if err != nil {
		return nil, errors.Wrapf(err,
			"failed to create plugin connection for plugin %q", name)
	}
	defer sockets.Close()

	conn, err := sockets.LocalConn()
	if err != nil {
		return nil, errors.Wrapf(err,
			"failed to set up local connection for plugin %q", name)
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

	p = &plugin{
		name:   name,
		cfg:    cfg,
		cmd:    cmd,
		regC:   make(chan error, 1),
		closeC: make(chan struct{}),
	}

	if err := p.connect(conn); err != nil {
		return nil, err
	}

	return p, nil
}

func newExternalPlugin(conn stdnet.Conn) (p *plugin, retErr error) {
	p = &plugin{
		regC:   make(chan error, 1),
		closeC: make(chan struct{}),
	}
	if err := p.connect(conn); err != nil {
		return nil, err
	}

	return p, nil
}

func (p *plugin) connect(conn stdnet.Conn) (retErr error) {
	mux := multiplex.Multiplex(conn, multiplex.WithBlockedRead())
	defer func() {
		if retErr != nil {
			mux.Close()
		}
	}()

	pconn, err := mux.Open(multiplex.PluginServiceConn)
	if err != nil {
		return errors.Wrapf(err,
			"failed to mux plugin connection for plugin %q", p.Name())
	}
	rpcc := ttrpc.NewClient(pconn, ttrpc.WithOnClose(
		func () {
			log.Infof(nil, "connection to plugin %s closed...", p.Name())
			close(p.closeC)
			p.close()
		}))
	defer func() {
		if retErr != nil {
			rpcc.Close()
		}
	}()
	stub := api.NewPluginClient(rpcc)

	rpcs, err := ttrpc.NewServer()
	if err != nil {
		return errors.Wrapf(err,
			"failed to create ttrpc server for plugin %q", p.Name())
	}
	defer func() {
		if retErr != nil {
			rpcs.Close()
		}
	}()

	rpcl, err := mux.Listen(multiplex.RuntimeServiceConn)
	if err != nil {
		return errors.Wrapf(err,
			"failed to create mux runtime listener for plugin %q", p.Name())
	}

	p.mux = mux
	p.rpcc = rpcc
	p.rpcl = rpcl
	p.rpcs = rpcs
	p.stub = stub

	return nil
}

func (p *plugin) start() error {
	var err error

	if p.cmd != nil {
		if err = p.cmd.Start(); err != nil {
			return errors.Wrapf(err, "failed launch plugin %q", p.Name())
		}
	}

	api.RegisterRuntimeService(p.rpcs, p)
	p.mux.Unblock()

	go func() {
		err := p.rpcs.Serve(context.Background(), p.rpcl)
		log.Infof(nil, "*** plugin server exited (%v)", err)
	}()

	select {
	case err = <- p.regC:
		if err != nil {
			return errors.Wrap(err, "failed to register plugin")
		}
	case <- p.closeC:
		return errors.Wrap(err, "failed to register plugin, connection closed")
	case <-time.After(pluginRegisterTimeout):
		return errors.Errorf("plugin registration timed out")
	}

	err = p.configure(context.Background(), p.cfg)
	if err != nil {
		p.stop()
		return err
	}

	return nil
}

func (p *plugin) close() {
	p.closed = true
	p.mux.Close()
	p.rpcc.Close()
	p.rpcs.Close()
	p.rpcl.Close()
}

func (p *plugin) stop() error {
	if p.cmd == nil || p.cmd.Process == nil {
		return nil
	}

	// XXX TODO:
	//   We should attempt a graceful shutdown of the process here...
	//     - send it SIGINT
	//     - give the it some slack waiting with a timeout
	//     - butcher it with SIGKILL after the timeout

	p.cmd.Process.Kill()
	p.cmd.Process.Wait()
	p.cmd.Process.Release()

	return nil
}


func (p *plugin) Name() string {
	name := p.name
	if name == "" {
		name = "external-plugin"
	}
	id := p.id
	if id == "" {
		id = "unknown"
	}
	return name+"/"+id+"["+strconv.Itoa(p.pid)+"]"
}

// RegisterPlugin handles plugin registration.
func (p *plugin) RegisterPlugin(ctx context.Context, req *RegisterPluginRequest) (*RegisterPluginResponse, error) {
	log.Infof(ctx, "*** received registration request from %q...", req.PluginId)

	p.id = req.PluginId
	close(p.regC)

	return &RegisterPluginResponse{}, nil
}

// AdjustContainers relays container adjustment request to the runtime.
func (p *plugin) AdjustContainers(ctx context.Context, req *AdjustContainersRequest) (*AdjustContainersResponse, error) {
	log.Infof(ctx, "*** adjust containers request...")
	return &AdjustContainersResponse{}, nil
}

// configure the plugin and subscribe it for the events it requests.
func (p *plugin) configure(ctx context.Context, config string) error {
	ctx, cancel := context.WithTimeout(ctx, pluginRequestTimeout)
	defer cancel()

	rpl, err := p.stub.Configure(ctx, &ConfigureRequest{
		Config: config,
	})
	if err != nil {
		return errors.Wrap(err, "failed to configure plugin")
	}

	eventMask := map[api.Event]int{
		api.Event_RUN_POD_SANDBOX:       runPodSandbox,
		api.Event_STOP_POD_SANDBOX:      stopPodSandbox,
		api.Event_REMOVE_POD_SANDBOX:    removePodSandbox,
		api.Event_CREATE_CONTAINER:      createContainer,
		api.Event_POST_CREATE_CONTAINER: postCreateContainer,
		api.Event_START_CONTAINER:       startContainer,
		api.Event_POST_START_CONTAINER:  postStartContainer,
		api.Event_UPDATE_CONTAINER:      updateContainer,
		api.Event_POST_UPDATE_CONTAINER: postUpdateContainer,
		api.Event_STOP_CONTAINER:        stopContainer,
		api.Event_REMOVE_CONTAINER:      removeContainer,
		api.Event_ALL:                   allEvents,
	}

	for _, event := range rpl.Subscribe {
		mask, ok := eventMask[event]
		if !ok {
			return errors.Errorf("invalid plugin event: %v", event)
		}
		p.events |= mask
	}

	if p.events == 0 {
		p.events = allEvents
	}

	if p.cmd == nil {
		p.pid, err = getPeerPid(p.mux.Trunk())
		if err != nil {
			return errors.Wrap(err, "failed to determine peer process")
		}
	}

	return nil
}

// synchronize synchronizes the plugin with the state of the runtime.
func (p *plugin) synchronize(ctx context.Context, pods []*PodSandbox, containers []*Container) ([]*ContainerAdjustment, error) {
	log.Infof(ctx, "NRI: synchronizing plugin %s", p.Name())

	ctx, cancel := context.WithTimeout(ctx, pluginRequestTimeout)
	defer cancel()

	req := &SynchronizeRequest{
		Pods:       pods,
		Containers: containers,
	}
	rpl, err := p.stub.Synchronize(ctx, req)
	if err != nil {
		return nil, err
	}

	return rpl.Adjust, nil
}

// Relay RunPodSandbox request to the plugin.
func (p *plugin) runPodSandbox(ctx context.Context, req *RunPodSandboxRequest) error {
	if !p.subcribedEvent(runPodSandbox) {
		return nil
	}

	_, err := p.stub.RunPodSandbox(ctx, req)
	if err == ttrpc.ErrClosed {
		p.close()
		err = nil
	}

	return err
}

// Relay StopPodSandbox request to the plugin.
func (p *plugin) stopPodSandbox(ctx context.Context, req *StopPodSandboxRequest) error {
	if !p.subcribedEvent(stopPodSandbox) {
		return nil
	}

	_, err := p.stub.StopPodSandbox(ctx, req)
	if err == ttrpc.ErrClosed {
		p.close()
		err = nil
	}

	return err
}

// Relay RemovePodSandbox request to the plugin.
func (p *plugin) removePodSandbox(ctx context.Context, req *RemovePodSandboxRequest) error {
	if !p.subcribedEvent(removePodSandbox) {
		return nil
	}

	_, err := p.stub.RemovePodSandbox(ctx, req)
	if err == ttrpc.ErrClosed {
		p.close()
		err = nil
	}

	return err
}

// Relay CreateContainer request to plugin.
func (p *plugin) createContainer(ctx context.Context, req *CreateContainerRequest) (*CreateContainerResponse, error) {
	if !p.subcribedEvent(createContainer) {
		return nil, nil
	}

	rpl, err := p.stub.CreateContainer(ctx, req)
	if err != nil {
		if err == ttrpc.ErrClosed {
			p.close()
			err = nil
		}
		return nil, err
	}

	return rpl, nil
}

// Relay postCreateContainer event to the plugin.
func (p *plugin) postCreateContainer(ctx context.Context, req *PostCreateContainerRequest) error {
	if !p.subcribedEvent(postCreateContainer) {
		return nil
	}

	_, err := p.stub.PostCreateContainer(ctx, req)
	if err == ttrpc.ErrClosed {
		p.close()
		err = nil
	}

	return err
}

// Relay StartContainer request to the plugin.
func (p *plugin) startContainer(ctx context.Context, req *StartContainerRequest) error {
	if !p.subcribedEvent(startContainer) {
		return nil
	}

	_, err := p.stub.StartContainer(ctx, req)
	if err == ttrpc.ErrClosed {
		p.close()
		err = nil
	}

	return err
}

// Relay postStartContainer event to the plugin.
func (p *plugin) postStartContainer(ctx context.Context, req *PostStartContainerRequest) error {
	if !p.subcribedEvent(postStartContainer) {
		return nil
	}

	_, err := p.stub.PostStartContainer(ctx, req)
	if err == ttrpc.ErrClosed {
		p.close()
		err = nil
	}

	return err
}

// Relay UpdateContainer request to plugin.
func (p *plugin) updateContainer(ctx context.Context, req *UpdateContainerRequest) (*UpdateContainerResponse, error) {
	if !p.subcribedEvent(updateContainer) {
		return nil, nil
	}

	rpl, err := p.stub.UpdateContainer(ctx, req)
	if err != nil {
		if err == ttrpc.ErrClosed {
			p.close()
			err = nil
		}
		return nil, err
	}

	return rpl, nil
}

// Relay postUpdateContainer event to the plugin.
func (p *plugin) postUpdateContainer(ctx context.Context, req *PostUpdateContainerRequest) error {
	if !p.subcribedEvent(postUpdateContainer) {
		return nil
	}

	_, err := p.stub.PostUpdateContainer(ctx, req)
	if err == ttrpc.ErrClosed {
		p.close()
		err = nil
	}

	return err
}

// Relay StopContainer request to plugin.
func (p *plugin) stopContainer(ctx context.Context, req *StopContainerRequest) (*StopContainerResponse, error) {
	if !p.subcribedEvent(stopContainer) {
		return nil, nil
	}

	rpl, err := p.stub.StopContainer(ctx, req)
	if err != nil {
		if err == ttrpc.ErrClosed {
			p.stop()
			err = nil
		}
		return nil, err
	}

	return rpl, nil
}

// Relay RemoveContainer request to the plugin.
func (p *plugin) removeContainer(ctx context.Context, req *RemoveContainerRequest) error {
	if !p.subcribedEvent(removeContainer) {
		return nil
	}
	_, err := p.stub.RemoveContainer(ctx, req)
	if err == ttrpc.ErrClosed {
		p.close()
		err = nil
	}

	return err
}

// subscribedEvent returns true if the plugin subcribes for the given event.
func (p *plugin) subcribedEvent(e int) bool {
	return p.events&e != 0
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
