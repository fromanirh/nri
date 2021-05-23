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
	"fmt"
	stdnet "net"

	"github.com/pkg/errors"

	api "github.com/containerd/nri/api/plugin/vproto"
	"github.com/containerd/nri/pkg/net/multiplex"
	"github.com/containerd/ttrpc"
)

type server struct {
	plugin   *plugin
	listener stdnet.Listener
	ttrpcs   *ttrpc.Server
}

func newServer(plugin *plugin) (*server, error) {
	ttrpcs, err := ttrpc.NewServer()
	if err != nil {
		return nil, errors.Wrap(err, "failed to create ttrpc server")
	}

	listener, err := plugin.mux.Listen(multiplex.RuntimeServiceConn)
	if err != nil {
		ttrpcs.Close()
		return nil, errors.Wrap(err, "failed to listen on runtime connection")
	}

	s := &server{
		plugin:   plugin,
		listener: listener,
		ttrpcs:   ttrpcs,
	}

	api.RegisterRuntimeService(s.ttrpcs, s)

	return s, nil
}

func (s *server) start() error {
	if s == nil || s.ttrpcs == nil {
		return nil
	}

	go func() {
		s.ttrpcs.Serve(context.Background(), s.listener)
	}()

	return nil
}

func (s *server) stop() error {
	if s != nil && s.ttrpcs != nil {
		return s.ttrpcs.Close()
	}
	return nil
}

func (s *server) AdjustContainers(ctx context.Context, req *api.AdjustContainersRequest) (*api.AdjustContainersResponse, error) {
	log.Infof("NRI: got (unsolicited) AdjustContainers request...")
	return &api.AdjustContainersResponse{}, nil
}

func (s *server) Ping(ctx context.Context, req *api.PingRequest) (*api.PingResponse, error) {
	seqNum := req.RequestNumber
	reqMsg := req.Msg
	sender := s.plugin.id

	log.Infof("NRI: got HELLO #%d from %s: %s", seqNum, sender, reqMsg)
	return &api.PingResponse{
		RequestNumber: seqNum,
		Msg: fmt.Sprintf("Ahoy #%d, matey %s there", seqNum, sender),
	}, nil
}

