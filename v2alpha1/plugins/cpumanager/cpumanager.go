/*
   Copyright The containerd Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an Sub"AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	cadvisorapi "github.com/google/cadvisor/info/v1"
	"github.com/pkg/errors"
	"github.com/spf13/pflag"

	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/kubelet/cm/containermap"
	"k8s.io/kubernetes/pkg/kubelet/cm/cpumanager"
	"k8s.io/kubernetes/pkg/kubelet/cm/cpumanager/state"
	"k8s.io/kubernetes/pkg/kubelet/cm/cpumanager/topology"
	"k8s.io/kubernetes/pkg/kubelet/cm/cpuset"
	"k8s.io/kubernetes/pkg/kubelet/cm/topologymanager"
	"sigs.k8s.io/yaml"

	"github.com/fromanirh/cpumgrx/pkg/machineinformer"

	"github.com/containerd/nri/v2alpha1/pkg/api"
	"github.com/containerd/nri/v2alpha1/pkg/stub"
)

const (
	programName   = "nri-cpumanager"
	stateFilename = "nri_cpumanager.state"
)

type nriTMStore struct{}

func (tm nriTMStore) GetAffinity(podUID string, containerName string) topologymanager.TopologyHint {
	return topologymanager.TopologyHint{}
}

type config struct {
	ReservedCPUs    string        `json:"reservedCPUs"`
	StateDirectory  string        `json:"stateDirectory"`
	SysfsRoot       string        `json:"sysfsRoot"`
	ReconcilePeriod time.Duration `json:"reconcilePeriod"`
}

func defaults() config {
	return config{
		ReconcilePeriod: 10 * time.Second,
		StateDirectory:  "/var/lib/nri-cpumanager",
		SysfsRoot:       "/",
	}
}

type plugin struct {
	stub         stub.Stub
	conf         config
	reservedCpus cpuset.CPUSet
	policy       cpumanager.Policy
	state        cpumanager.State
	machineInfo  *cadvisorapi.MachineInfo
	containers   []*api.Container
	sr           nriSourcesReady
	tm           nriTMStore
}

func (p *plugin) Configure(nriCfg string) (stub.SubscribeMask, error) {
	p.conf = defaults()
	err := yaml.Unmarshal([]byte(nriCfg), &p.conf)
	if err != nil {
		return 0, errors.Wrap(err, "failed to parse provided configuration")
	}

	cpus, err := cpuset.Parse(p.conf.ReservedCPUs)
	if err != nil {
		return 0, errors.Wrap(err, "failed to parse the reserved CPU set")
	}
	p.reservedCpus = cpus

	machineInfo, err := machineinformer.GetRaw(p.conf.SysfsRoot)
	if err != nil {
		return nil, err
	}

	p.machineInfo = machineInfo
	topo, err := topology.Discover(p.machineInfo)
	if err != nil {
		return nil, err
	}
	klog.InfoS("Detected CPU topology", "topology", topo)

	if p.reservedCpus.Size() == 0 {
		return 0, fmt.Errorf("the static policy requires reserved cpus, got none")
	}

	// Take the ceiling of the reservation, since fractional CPUs cannot be
	// exclusively allocated.
	policy, err = cpumanager.NewStaticPolicy(topo, p.reservedCPUs.Size(), p.reservedCPUs, p.tm)
	if err != nil {
		return 0, errors.Wrap(err, "failed to create the static policy")
	}

	stateImpl, err := state.NewCheckpointState(p.conf.stateDirectory, stateFileName, p.policy.Name(), containermap.ContainerMap{})
	if err != nil {
		klog.ErrorS(err, "Could not initialize checkpoint manager, please drain node and remove policy state file")
		return 0, err
	}
	p.state = stateImpl

	err = p.policy.Start(p.state)
	if err != nil {
		klog.ErrorS(err, "Policy start error")
		return err
	}

	mask := stub.SubscribeMask(
		stub.CreateContainer | stub.PostCreateContainer | stub.StartContainer | stub.PostStartContainer | stub.UpdateContainer | stub.PostUpdateContainer | stub.StopContainer | stub.RemoveContainer,
	)
	return mask, nil
}

func (p *plugin) Synchronize(pods []*api.PodSandbox, containers []*api.Container) ([]*api.ContainerAdjustment, error) {
	p.containers = make([]*api.Container, len(containers))
	copy(p.containers, containers)
	// TODO: reconcile, just in case?
	return nil, nil
}

func (p *plugin) Shutdown() {
}

//Plugin output:
//modified container information for the container being created
//modified container information for existing/running containers
func (p *plugin) CreateContainer(pod *api.PodSandbox, container *api.Container) (*api.ContainerCreateAdjustment, []*api.ContainerAdjustment, error) {
	cpus, err := p.cpuManager.Allocate()
	return nil, nil, nil
}

func (p *plugin) PostCreateContainer(pod *api.PodSandbox, container *api.Container) {
}

func (p *plugin) StartContainer(pod *api.PodSandbox, container *api.Container) {
}

func (p *plugin) PostStartContainer(pod *api.PodSandbox, container *api.Container) {
}

func (p *plugin) UpdateContainer(pod *api.PodSandbox, container *api.Container) ([]*api.ContainerAdjustment, error) {
	return nil, nil
}

func (p *plugin) PostUpdateContainer(pod *api.PodSandbox, container *api.Container) {
}

func (p *plugin) StopContainer(pod *api.PodSandbox, container *api.Container) ([]*api.ContainerAdjustment, error) {
	return nil, nil
}

func (p *plugin) RemoveContainer(pod *api.PodSandbox, container *api.Container) {
}

func (p *plugin) RunPodSandbox(pod *api.PodSandbox) {
}

func (p *plugin) StopPodSandbox(pod *api.PodSandbox) {
}

func (p *plugin) RemovePodSandbox(pod *api.PodSandbox) {
}

func (p *plugin) onClose() {
	os.Exit(0)
}

func main() {
	sysfsRoot := ""
	dumpInfo := false

	flags := flag.NewFlagSet(programName, flag.ExitOnError)
	klog.InitFlags(flags)

	pflag.StringVarP(&sysfsRoot, "root-dir", "r", "", "use <arg> as root prefix - use this if run inside a container")
	pflag.BoolVarP(&dumpInfo, "dump-info", "D", false, "dump machineinfo and exits")
	pflag.Parse()

	if dumpInfo {
		info, err := machineinformer.GetRaw(sysfsRoot)
		if err != nil {
			klog.Fatalf("Cannot get machine info: %v")
		}

		json.NewEncoder(os.Stdout).Encode(info)
		os.Exit(0)
	}

	p := &plugin{}
	opts := []stub.Option{
		stub.WithOnClose(p.onClose),
		stub.WithPluginName("cpumanager"),
		stub.WithPluginID("50"),
	}

	s, err := stub.New(p, opts...)
	if err != nil {
		klog.Fatalf("failed to create plugin stub: %v", err)
	}
	p.stub = s

	err = p.stub.Run(context.Background())
	if err != nil {
		klog.Errorf("Plugin exited with error %v", err)
	}
}
