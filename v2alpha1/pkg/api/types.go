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

package api

import (
	"io/fs"
	rspec "github.com/opencontainers/runtime-spec/specs-go"
)

const (
	ContainerStateCreated = ContainerState_CONTAINER_CREATED
	ContainerStateRunning = ContainerState_CONTAINER_RUNNING
	ContainerStateExited  = ContainerState_CONTAINER_EXITED
)

type (
	RunPodSandboxRequest     = RunPodSandboxEvent
	RunPodSandboxResponse    = Empty
	StopPodSandboxRequest    = StopPodSandboxEvent
	StopPodSandboxResponse   = Empty
	RemovePodSandboxRequest  = RemovePodSandboxEvent
	RemovePodSandboxResponse = Empty

	StartContainerRequest   = StartContainerEvent
	StartContainerResponse  = Empty
	RemoveContainerRequest  = RemoveContainerEvent
	RemoveContainerResponse = Empty

	PostCreateContainerRequest  = PostCreateContainerEvent
	PostCreateContainerResponse = Empty
	PostStartContainerRequest   = PostStartContainerEvent
	PostStartContainerResponse  = Empty
	PostUpdateContainerRequest  = PostUpdateContainerEvent
	PostUpdateContainerResponse = Empty

	ShutdownRequest  = Empty
	ShutdownResponse = Empty
)

func (hooks *Hooks) Append(h *Hooks) *Hooks {
	if h == nil {
		return hooks
	}
	hooks.Prestart = append(hooks.Prestart, h.Prestart...)
	hooks.CreateRuntime = append(hooks.CreateRuntime, h.CreateRuntime...)
	hooks.CreateContainer = append(hooks.CreateContainer, h.CreateContainer...)
	hooks.StartContainer = append(hooks.StartContainer, h.StartContainer...)
	hooks.Poststart = append(hooks.Poststart, h.Poststart...)
	hooks.Poststop = append(hooks.Poststop, h.Poststop...)

	return hooks
}

func (h *Hooks) Hooks() *Hooks {
	if h == nil {
		return nil
	}

	if len(h.Prestart) > 0 {
		return h
	}
	if len(h.CreateRuntime) > 0 {
		return h
	}
	if len(h.CreateContainer) > 0 {
		return h
	}
	if len(h.StartContainer) > 0 {
		return h
	}
	if len(h.Poststart) > 0 {
		return h
	}
	if len(h.Poststop) > 0 {
		return h
	}

	return nil
}

func (m *Mount) Cmp(v *Mount) bool {
	if v == nil {
		return false
	}
	return m.ContainerPath != v.ContainerPath || m.HostPath != v.HostPath ||
		m.Readonly != v.Readonly ||	m.SelinuxRelabel != v.SelinuxRelabel ||
		m.Propagation != v.Propagation
}

func (d *Device) Cmp(v *Device) bool {
	if v == nil {
		return false
	}
	return d.ContainerPath != v.ContainerPath || d.HostPath != v.HostPath ||
		d.Permissions != v.Permissions
}

func SpecFromOCI(o *rspec.Spec) *Spec {
	return &Spec{
		Process:     processFromOCI(o.Process),
		Mounts:      mountsFromOCI(o.Mounts),
		Hooks:       hooksFromOCI(o.Hooks),
		Annotations: dupStringMap(o.Annotations),
		Linux:       linuxFromOCI(o.Linux),
	}
}

func processFromOCI(o *rspec.Process) *Process {
	if o == nil {
		return nil
	}
	return &Process{
		Args:        dupStringSlice(o.Args),
		Env:         dupStringSlice(o.Env),
		OomScoreAdj: intValue(o.OOMScoreAdj),
	}
}

func mountsFromOCI(o []rspec.Mount) []*OCIMount {
	var mounts []*OCIMount
	for _, m := range o {
		mounts = append(mounts, &OCIMount{
			Destination: m.Destination,
			Type:        m.Type,
			Source:      m.Source,
			Options:     dupStringSlice(m.Options),
		})
	}
	return mounts
}

func hooksFromOCI(o *rspec.Hooks) *Hooks {
	if o == nil {
		return nil
	}
	return &Hooks{
		Prestart:        hookSliceFromOCI(o.Prestart),
		CreateRuntime:   hookSliceFromOCI(o.CreateRuntime),
		CreateContainer: hookSliceFromOCI(o.CreateContainer),
		StartContainer:  hookSliceFromOCI(o.StartContainer),
		Poststart:       hookSliceFromOCI(o.Poststart),
		Poststop:        hookSliceFromOCI(o.Poststop),
	}
}

func hookSliceFromOCI(o []rspec.Hook) []*Hook {
	var hooks []*Hook
	for _, h := range o {
		hooks = append(hooks, &Hook{
			Path:    h.Path,
			Args:    dupStringSlice(h.Args),
			Env:     dupStringSlice(h.Env),
			Timeout: intValue(h.Timeout),
		})
	}
	return hooks
}

func linuxFromOCI(o *rspec.Linux) *Linux {
	if o == nil {
		return nil
	}
	l := &Linux{
		Resources:   linuxResourcesFromOCI(o.Resources),
		CgroupsPath: o.CgroupsPath,
	}
	for _, n := range o.Namespaces {
		l.Namespaces = append(l.Namespaces, &LinuxNamespace{
			Type: string(n.Type),
			Path: n.Path,
		})
	}
	for _, d := range o.Devices {
		l.Devices = append(l.Devices, &LinuxDevice{
			Path:     d.Path,
			Type:     d.Type,
			Major:    d.Major,
			Minor:    d.Minor,
			FileMode: fileModeValue(d.FileMode),
			Uid:      uint32Value(d.UID),
			Gid:      uint32Value(d.GID),
		})
	}
	return l
}

func linuxResourcesFromOCI(o *rspec.LinuxResources) *LinuxResources {
	if o == nil {
		return nil
	}
	l := &LinuxResources{}
	for _, d := range o.Devices {
		l.Devices = append(l.Devices, &LinuxDeviceCgroup{
			Allow:  d.Allow,
			Type:   d.Type,
			Major:  int64Value(d.Major),
			Minor:  int64Value(d.Minor),
			Access: d.Access,
		})
	}
	if m := o.Memory; m != nil {
		l.Memory = &LinuxMemory{
			Limit:            int64Value(m.Limit),
			Reservation:      int64Value(m.Reservation),
			Swap:             int64Value(m.Swap),
			Kernel:           int64Value(m.Kernel),
			KernelTcp:        int64Value(m.KernelTCP),
			Swappiness:       uint64Value(m.Swappiness),
			DisableOomKiller: boolValue(m.DisableOOMKiller),
			UseHierarchy:     boolValue(m.UseHierarchy),
		}
	}
	if c := o.CPU; c != nil {
		l.Cpu = &LinuxCPU{
			Shares:          uint64Value(c.Shares),
			Quota:           int64Value(c.Quota),
			Period:          uint64Value(c.Period),
			RealtimeRuntime: int64Value(c.RealtimeRuntime),
			RealtimePeriod:  uint64Value(c.RealtimePeriod),
			Cpus:            c.Cpus,
			Mems:            c.Mems,
		}
	}
	for _, h := range o.HugepageLimits {
		l.HugepageLimits = append(l.HugepageLimits, &HugepageLimit{
			PageSize: h.Pagesize,
			Limit:    h.Limit,
		})
	}
	return l
}

func dupStringSlice(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func dupStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := map[string]string{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

func intValue(v *int) *IntValue {
	if v == nil {
		return nil
	}
	return &IntValue {
		Value: int64(*v),
	}
}

func int32Value(v *int32) *Int32Value {
	if v == nil {
		return nil
	}
	return &Int32Value{
		Value: *v,
	}
}

func fileModeValue(v *fs.FileMode) *UInt32Value {
	if v == nil {
		return nil
	}
	return &UInt32Value{
		Value: uint32(v.Perm()),
	}
}

func uint32Value(v *uint32) *UInt32Value {
	if v == nil {
		return nil
	}
	return &UInt32Value{
		Value: *v,
	}
}

func int64Value(v *int64) *Int64Value {
	if v == nil {
		return nil
	}
	return &Int64Value{
		Value: *v,
	}
}

func uint64Value(v *uint64) *UInt64Value {
	if v == nil {
		return nil
	}
	return &UInt64Value{
		Value: *v,
	}
}

func boolValue(v *bool) *BoolValue {
	if v == nil {
		return nil
	}
	return &BoolValue{
		Value: *v,
	}
}
