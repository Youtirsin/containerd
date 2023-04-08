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

package podsandbox

import (
	"strconv"
	"testing"

	imagespec "github.com/opencontainers/image-spec/specs-go/v1"
	runtimespec "github.com/opencontainers/runtime-spec/specs-go"
	"github.com/opencontainers/selinux/go-selinux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	runtime "k8s.io/cri-api/pkg/apis/runtime/v1"
	v1 "k8s.io/cri-api/pkg/apis/runtime/v1"

	"github.com/containerd/containerd/pkg/cri/annotations"
	"github.com/containerd/containerd/pkg/cri/opts"
)

func getRunPodSandboxTestData() (*runtime.PodSandboxConfig, *imagespec.ImageConfig, func(*testing.T, string, *runtimespec.Spec)) {
	config := &runtime.PodSandboxConfig{
		Metadata: &runtime.PodSandboxMetadata{
			Name:      "test-name",
			Uid:       "test-uid",
			Namespace: "test-ns",
			Attempt:   1,
		},
		Hostname:     "test-hostname",
		LogDirectory: "test-log-directory",
		Labels:       map[string]string{"a": "b"},
		Annotations:  map[string]string{"c": "d"},
		Linux: &runtime.LinuxPodSandboxConfig{
			CgroupParent: "/test/cgroup/parent",
		},
	}
	imageConfig := &imagespec.ImageConfig{
		Env:        []string{"a=b", "c=d"},
		Entrypoint: []string{"/pause"},
		Cmd:        []string{"forever"},
		WorkingDir: "/workspace",
	}
	specCheck := func(t *testing.T, id string, spec *runtimespec.Spec) {
		assert.Equal(t, "test-hostname", spec.Hostname)
		assert.Equal(t, getCgroupsPath("/test/cgroup/parent", id), spec.Linux.CgroupsPath)
		assert.Equal(t, relativeRootfsPath, spec.Root.Path)
		assert.Equal(t, true, spec.Root.Readonly)
		assert.Contains(t, spec.Process.Env, "a=b", "c=d")
		assert.Equal(t, []string{"/pause", "forever"}, spec.Process.Args)
		assert.Equal(t, "/workspace", spec.Process.Cwd)
		assert.EqualValues(t, *spec.Linux.Resources.CPU.Shares, opts.DefaultSandboxCPUshares)
		assert.EqualValues(t, *spec.Process.OOMScoreAdj, defaultSandboxOOMAdj)

		t.Logf("Check PodSandbox annotations")
		assert.Contains(t, spec.Annotations, annotations.SandboxID)
		assert.EqualValues(t, spec.Annotations[annotations.SandboxID], id)

		assert.Contains(t, spec.Annotations, annotations.ContainerType)
		assert.EqualValues(t, spec.Annotations[annotations.ContainerType], annotations.ContainerTypeSandbox)

		assert.Contains(t, spec.Annotations, annotations.SandboxNamespace)
		assert.EqualValues(t, spec.Annotations[annotations.SandboxNamespace], "test-ns")

		assert.Contains(t, spec.Annotations, annotations.SandboxUID)
		assert.EqualValues(t, spec.Annotations[annotations.SandboxUID], "test-uid")

		assert.Contains(t, spec.Annotations, annotations.SandboxName)
		assert.EqualValues(t, spec.Annotations[annotations.SandboxName], "test-name")

		assert.Contains(t, spec.Annotations, annotations.SandboxLogDir)
		assert.EqualValues(t, spec.Annotations[annotations.SandboxLogDir], "test-log-directory")

		if selinux.GetEnabled() {
			assert.NotEqual(t, "", spec.Process.SelinuxLabel)
			assert.NotEqual(t, "", spec.Linux.MountLabel)
		}
	}
	return config, imageConfig, specCheck
}

func TestLinuxSandboxContainerSpec(t *testing.T) {
	testID := "test-id"
	nsPath := "test-cni"
	for desc, test := range map[string]struct {
		configChange func(*runtime.PodSandboxConfig)
		specCheck    func(*testing.T, *runtimespec.Spec)
		expectErr    bool
	}{
		"spec should reflect original config": {
			specCheck: func(t *testing.T, spec *runtimespec.Spec) {
				// runtime spec should have expected namespaces enabled by default.
				require.NotNil(t, spec.Linux)
				assert.Contains(t, spec.Linux.Namespaces, runtimespec.LinuxNamespace{
					Type: runtimespec.NetworkNamespace,
					Path: nsPath,
				})
				assert.Contains(t, spec.Linux.Namespaces, runtimespec.LinuxNamespace{
					Type: runtimespec.UTSNamespace,
				})
				assert.Contains(t, spec.Linux.Namespaces, runtimespec.LinuxNamespace{
					Type: runtimespec.PIDNamespace,
				})
				assert.Contains(t, spec.Linux.Namespaces, runtimespec.LinuxNamespace{
					Type: runtimespec.IPCNamespace,
				})
				assert.Contains(t, spec.Linux.Sysctl["net.ipv4.ip_unprivileged_port_start"], "0")
				assert.Contains(t, spec.Linux.Sysctl["net.ipv4.ping_group_range"], "0 2147483647")
			},
		},
		"host namespace": {
			configChange: func(c *runtime.PodSandboxConfig) {
				c.Linux.SecurityContext = &runtime.LinuxSandboxSecurityContext{
					NamespaceOptions: &runtime.NamespaceOption{
						Network: runtime.NamespaceMode_NODE,
						Pid:     runtime.NamespaceMode_NODE,
						Ipc:     runtime.NamespaceMode_NODE,
					},
				}
			},
			specCheck: func(t *testing.T, spec *runtimespec.Spec) {
				// runtime spec should disable expected namespaces in host mode.
				require.NotNil(t, spec.Linux)
				assert.NotContains(t, spec.Linux.Namespaces, runtimespec.LinuxNamespace{
					Type: runtimespec.NetworkNamespace,
				})
				assert.NotContains(t, spec.Linux.Namespaces, runtimespec.LinuxNamespace{
					Type: runtimespec.UTSNamespace,
				})
				assert.NotContains(t, spec.Linux.Namespaces, runtimespec.LinuxNamespace{
					Type: runtimespec.PIDNamespace,
				})
				assert.NotContains(t, spec.Linux.Namespaces, runtimespec.LinuxNamespace{
					Type: runtimespec.IPCNamespace,
				})
				assert.NotContains(t, spec.Linux.Sysctl["net.ipv4.ip_unprivileged_port_start"], "0")
				assert.NotContains(t, spec.Linux.Sysctl["net.ipv4.ping_group_range"], "0 2147483647")
			},
		},
		"should set supplemental groups correctly": {
			configChange: func(c *runtime.PodSandboxConfig) {
				c.Linux.SecurityContext = &runtime.LinuxSandboxSecurityContext{
					SupplementalGroups: []int64{1111, 2222},
				}
			},
			specCheck: func(t *testing.T, spec *runtimespec.Spec) {
				require.NotNil(t, spec.Process)
				assert.Contains(t, spec.Process.User.AdditionalGids, uint32(1111))
				assert.Contains(t, spec.Process.User.AdditionalGids, uint32(2222))
			},
		},
		"should overwrite default sysctls": {
			configChange: func(c *runtime.PodSandboxConfig) {
				c.Linux.Sysctls = map[string]string{
					"net.ipv4.ip_unprivileged_port_start": "500",
					"net.ipv4.ping_group_range":           "1 1000",
				}
			},
			specCheck: func(t *testing.T, spec *runtimespec.Spec) {
				require.NotNil(t, spec.Process)
				assert.Contains(t, spec.Linux.Sysctl["net.ipv4.ip_unprivileged_port_start"], "500")
				assert.Contains(t, spec.Linux.Sysctl["net.ipv4.ping_group_range"], "1 1000")
			},
		},
		"sandbox sizing annotations should be set if LinuxContainerResources were provided": {
			configChange: func(c *runtime.PodSandboxConfig) {
				c.Linux.Resources = &v1.LinuxContainerResources{
					CpuPeriod:          100,
					CpuQuota:           200,
					CpuShares:          5000,
					MemoryLimitInBytes: 1024,
				}
			},
			specCheck: func(t *testing.T, spec *runtimespec.Spec) {
				value, ok := spec.Annotations[annotations.SandboxCPUPeriod]
				assert.True(t, ok)
				assert.EqualValues(t, strconv.FormatInt(100, 10), value)
				assert.EqualValues(t, "100", value)

				value, ok = spec.Annotations[annotations.SandboxCPUQuota]
				assert.True(t, ok)
				assert.EqualValues(t, "200", value)

				value, ok = spec.Annotations[annotations.SandboxCPUShares]
				assert.True(t, ok)
				assert.EqualValues(t, "5000", value)

				value, ok = spec.Annotations[annotations.SandboxMem]
				assert.True(t, ok)
				assert.EqualValues(t, "1024", value)
			},
		},
		"sandbox sizing annotations should not be set if LinuxContainerResources were not provided": {
			specCheck: func(t *testing.T, spec *runtimespec.Spec) {
				_, ok := spec.Annotations[annotations.SandboxCPUPeriod]
				assert.False(t, ok)
				_, ok = spec.Annotations[annotations.SandboxCPUQuota]
				assert.False(t, ok)
				_, ok = spec.Annotations[annotations.SandboxCPUShares]
				assert.False(t, ok)
				_, ok = spec.Annotations[annotations.SandboxMem]
				assert.False(t, ok)
			},
		},
		"sandbox sizing annotations are zero if the resources are set to 0": {
			configChange: func(c *runtime.PodSandboxConfig) {
				c.Linux.Resources = &v1.LinuxContainerResources{}
			},
			specCheck: func(t *testing.T, spec *runtimespec.Spec) {
				value, ok := spec.Annotations[annotations.SandboxCPUPeriod]
				assert.True(t, ok)
				assert.EqualValues(t, "0", value)
				value, ok = spec.Annotations[annotations.SandboxCPUQuota]
				assert.True(t, ok)
				assert.EqualValues(t, "0", value)
				value, ok = spec.Annotations[annotations.SandboxCPUShares]
				assert.True(t, ok)
				assert.EqualValues(t, "0", value)
				value, ok = spec.Annotations[annotations.SandboxMem]
				assert.True(t, ok)
				assert.EqualValues(t, "0", value)
			},
		},
	} {
		t.Run(desc, func(t *testing.T) {
			c := newControllerService()
			c.config.EnableUnprivilegedICMP = true
			c.config.EnableUnprivilegedPorts = true
			config, imageConfig, specCheck := getRunPodSandboxTestData()
			if test.configChange != nil {
				test.configChange(config)
			}
			spec, err := c.sandboxContainerSpec(testID, config, imageConfig, nsPath, nil)
			if test.expectErr {
				assert.Error(t, err)
				assert.Nil(t, spec)
				return
			}
			assert.NoError(t, err)
			assert.NotNil(t, spec)
			specCheck(t, testID, spec)
			if test.specCheck != nil {
				test.specCheck(t, spec)
			}
		})
	}
}

func TestSandboxDisableCgroup(t *testing.T) {
	config, imageConfig, _ := getRunPodSandboxTestData()
	c := newControllerService()
	c.config.DisableCgroup = true
	spec, err := c.sandboxContainerSpec("test-id", config, imageConfig, "test-cni", []string{})
	require.NoError(t, err)

	t.Log("resource limit should not be set")
	assert.Nil(t, spec.Linux.Resources.Memory)
	assert.Nil(t, spec.Linux.Resources.CPU)

	t.Log("cgroup path should be empty")
	assert.Empty(t, spec.Linux.CgroupsPath)
}

// TODO(random-liu): [P1] Add unit test for different error cases to make sure
// the function cleans up on error properly.
