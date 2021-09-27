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

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1alpha2"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/kubelet/cm/containermap"
	"k8s.io/kubernetes/pkg/kubelet/cm/cpumanager"
	"k8s.io/kubernetes/pkg/kubelet/cm/cpuset"
	"k8s.io/kubernetes/pkg/kubelet/cm/topologymanager"
	"sigs.k8s.io/yaml"

	"github.com/fromanirh/cpumgrx/pkg/machineinformer"

	"github.com/containerd/nri/v2alpha1/pkg/api"
	"github.com/containerd/nri/v2alpha1/pkg/stub"
)

const (
	programName = "nri-cpumanager"
	policyName  = "static"
)

type nriTMStore struct{}

func (tm nriTMStore) GetAffinity(podUID string, containerName string) topologymanager.TopologyHint {
	return topologymanager.TopologyHint{}
}

type nriSourcesReady struct{}

func (s nriSourcesReady) AddSource(source string) {}
func (s nriSourcesReady) AllReady() bool          { return true }

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
	cpuManager   cpumanager.Manager
	machineInfo  *cadvisorapi.MachineInfo
	containers   []*api.Container
	sr           nriSourcesReady
	tm           nriTMStore
}

func (p *plugin) activePods() []*v1.Pod {
	// TODO
	return []*v1.Pod{}
}

func (p *plugin) UpdateContainerResources(id string, resources *runtimeapi.LinuxContainerResources) error {
	// TODO
	return nil
}

func (p *plugin) GetPodStatus(uid types.UID) (v1.PodStatus, bool) {
	// returning false makes the caller skip, and we always want that.
	// This is the easiest (but noisy) way to disable the reconcile loop.
	return v1.PodStatus{}, false
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
	p.machineInfo = machineInfo

	// TODO
	reservedCPUQty := resource.MustParse(fmt.Sprintf("%d", p.reservedCpus.Size()))
	nodeAllocatableReservation := v1.ResourceList{
		v1.ResourceCPU: reservedCPUQty,
	}
	mgr, err := cpumanager.NewManager(policyName, p.conf.ReconcilePeriod, p.machineInfo, p.reservedCpus, nodeAllocatableReservation, p.conf.StateDirectory, p.tm)
	if err != nil {
		return 0, err

	}
	p.cpuManager = mgr

	initialContainers := containermap.ContainerMap{}
	if err := p.cpuManager.Start(p.activePods, p.sr, p, p, initialContainers); err != nil {
		return 0, err
	}

	mask := stub.SubscribeMask(
		stub.CreateContainer | stub.PostCreateContainer | stub.StartContainer | stub.PostStartContainer | stub.UpdateContainer | stub.PostUpdateContainer | stub.StopContainer | stub.RemoveContainer,
	)
	return mask, nil
}

/*
	// Called to trigger the allocation of CPUs to a container. This must be
	// called at some point prior to the AddContainer() call for a container,
	// e.g. at pod admission time.
	Allocate(pod *v1.Pod, container *v1.Container) error

	// AddContainer adds the mapping between container ID to pod UID and the container name
	// The mapping used to remove the CPU allocation during the container removal
	AddContainer(p *v1.Pod, c *v1.Container, containerID string)

	// RemoveContainer is called after Kubelet decides to kill or delete a
	// container. After this call, the CPU manager stops trying to reconcile
	// that container and any CPUs dedicated to the container are freed.
	RemoveContainer(containerID string) error
*/

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
