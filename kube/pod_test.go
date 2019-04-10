package kube

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/hpcloud/fissile/model"
	"github.com/stretchr/testify/assert"
	"k8s.io/client-go/pkg/api/resource"
	"k8s.io/client-go/pkg/api/v1"
	"k8s.io/client-go/pkg/util/intstr"
)

func podTestLoadRole(assert *assert.Assertions) *model.Role {
	workDir, err := os.Getwd()
	if !assert.NoError(err) {
		return nil
	}

	manifestPath := filepath.Join(workDir, "../test-assets/role-manifests/volumes.yml")
	releasePath := filepath.Join(workDir, "../test-assets/tor-boshrelease")
	releasePathBoshCache := filepath.Join(releasePath, "bosh-cache")
	release, err := model.NewDevRelease(releasePath, "", "", releasePathBoshCache)
	if !assert.NoError(err) {
		return nil
	}
	manifest, err := model.LoadRoleManifest(manifestPath, []*model.Release{release}, false)
	if !assert.NoError(err) {
		return nil
	}

	var role *model.Role
	for _, r := range manifest.Roles {
		if r != nil {
			if r.Name == "myrole" {
				role = r
			}
		}
	}
	if !assert.NotNil(role, "Failed to find role in manifest") {
		return nil
	}

	return role
}

func TestPodGetVolumes(t *testing.T) {
	assert := assert.New(t)
	role := podTestLoadRole(assert)
	if role == nil {
		return
	}

	claims := getVolumeClaims(role)

	assert.Len(claims, 2, "expected two claims")

	var persistentClaim, sharedClaim v1.PersistentVolumeClaim
	for _, claim := range claims {
		switch claim.GetName() {
		case role.Run.PersistentVolumes[0].Tag:
			persistentClaim = claim
		case role.Run.SharedVolumes[0].Tag:
			sharedClaim = claim
		default:
			assert.Fail("Got unexpected claim", "%v", claim)
		}
	}

	if assert.NotNil(persistentClaim) {
		assert.Contains(persistentClaim.Annotations, VolumeStorageClassAnnotation)
		assert.Equal("persistent", persistentClaim.Annotations[VolumeStorageClassAnnotation])
		assert.Equal([]v1.PersistentVolumeAccessMode{v1.ReadWriteOnce}, persistentClaim.Spec.AccessModes)
		if assert.NotNil(persistentClaim.Spec.Resources.Requests) {
			requests := persistentClaim.Spec.Resources.Requests
			if assert.Contains(requests, v1.ResourceStorage) {
				quantity := requests[v1.ResourceStorage]
				assert.Zero(resource.NewScaledQuantity(5, resource.Giga).Cmp(quantity),
					"Storage request %s should be 5 Gigs", quantity.String())
			}
		}
	}

	if assert.NotNil(sharedClaim) {
		assert.Contains(sharedClaim.Annotations, VolumeStorageClassAnnotation)
		assert.Equal("shared", sharedClaim.Annotations[VolumeStorageClassAnnotation])
		assert.Equal([]v1.PersistentVolumeAccessMode{v1.ReadWriteMany}, sharedClaim.Spec.AccessModes)
		if assert.NotNil(sharedClaim.Spec.Resources.Requests) {
			requests := sharedClaim.Spec.Resources.Requests
			if assert.Contains(requests, v1.ResourceStorage) {
				quantity := requests[v1.ResourceStorage]
				assert.Zero(resource.NewScaledQuantity(40, resource.Giga).Cmp(quantity),
					"Storage request %s should be 40 Gigs", quantity.String())
			}
		}
	}
}

func TestPodGetVolumeMounts(t *testing.T) {
	assert := assert.New(t)
	role := podTestLoadRole(assert)
	if role == nil {
		return
	}

	volumeMounts := getVolumeMounts(role)
	assert.Len(volumeMounts, 2)

	var persistentMount, sharedMount v1.VolumeMount
	for _, mount := range volumeMounts {
		switch mount.Name {
		case "persistent-volume":
			persistentMount = mount
		case "shared-volume":
			sharedMount = mount
		default:
			assert.Fail("Got unexpected volume mount", "%+v", mount)
		}
	}
	assert.Equal("persistent-volume", persistentMount.Name)
	assert.Equal("/mnt/persistent", persistentMount.MountPath)
	assert.False(persistentMount.ReadOnly)
	assert.Equal("shared-volume", sharedMount.Name)
	assert.Equal("/mnt/shared", sharedMount.MountPath)
	assert.False(sharedMount.ReadOnly)
}

func TestPodGetEnvVars(t *testing.T) {
	assert := assert.New(t)
	role := podTestLoadRole(assert)
	if role == nil {
		return
	}

	if !assert.Equal(1, role.Jobs.Len(), "Role should have one job") {
		return
	}

	role.Jobs[0].Properties = []*model.JobProperty{
		&model.JobProperty{
			Name: "some-property",
		},
	}

	role.Configuration.Templates["property.some-property"] = "((SOME_VAR))"

	samples := []struct {
		desc     string
		input    string
		expected string
	}{
		{
			desc:     "Simple string",
			input:    "simple string",
			expected: "simple string",
		},
		{
			desc:     "string with newline",
			input:    `hello\nworld`,
			expected: "hello\nworld",
		},
	}

	for _, sample := range samples {
		defaults := map[string]string{"SOME_VAR": sample.input}

		vars, err := getEnvVars(role, defaults)
		assert.NoError(err)
		assert.NotEmpty(vars)

		found := false
		for _, result := range vars {
			if result.Name == "SOME_VAR" {
				found = true
				assert.Equal(sample.expected, result.Value)
			}
		}
		assert.True(found, "failed to find expected variable")
	}
}

func TestPodGetContainerPorts(t *testing.T) {
	assert := assert.New(t)
	role := podTestLoadRole(assert)
	if role == nil {
		return
	}

	samples := []struct {
		desc     string
		ports    []*model.RoleRunExposedPort
		expected []v1.ContainerPort
		err      string
	}{
		{
			desc:     "Empty role should have no ports",
			ports:    []*model.RoleRunExposedPort{},
			expected: []v1.ContainerPort{},
		},
		{
			desc: "TCP port should be TCP",
			ports: []*model.RoleRunExposedPort{{
				Name:     "tcp-port",
				Protocol: "TcP",
				Internal: "1234",
			}},
			expected: []v1.ContainerPort{{
				Name:          "tcp-port",
				ContainerPort: 1234,
				Protocol:      v1.ProtocolTCP,
			}},
		},
		{
			desc: "UDP port should be UDP",
			ports: []*model.RoleRunExposedPort{{
				Name:     "udp-port",
				Protocol: "uDp",
				Internal: "1234",
			}},
			expected: []v1.ContainerPort{{
				Name:          "udp-port",
				ContainerPort: 1234,
				Protocol:      v1.ProtocolUDP,
			}},
		},
		{
			desc: "Long port names should be fixed",
			ports: []*model.RoleRunExposedPort{{
				Name:     "port-with-a-very-long-name",
				Protocol: "tcp",
				Internal: "4321",
			}},
			expected: []v1.ContainerPort{{
				Name:          "port-wi40a84c6a",
				ContainerPort: 4321,
				Protocol:      v1.ProtocolTCP,
			}},
		},
		{
			desc: "Odd port names should be sanitized",
			ports: []*model.RoleRunExposedPort{{
				Name:     "-!port@NAME$--$here#-%Ｕｎｉｃｏｄｅ*",
				Protocol: "tcp",
				Internal: "1234",
			}},
			expected: []v1.ContainerPort{{
				Name:          "portNAME-here",
				ContainerPort: 1234,
				Protocol:      v1.ProtocolTCP,
			}},
		},
		{
			desc: "Invalid port names should be rejected",
			ports: []*model.RoleRunExposedPort{{
				Name:     "-!-@-#-$-%-^-&-*-(-)-",
				Protocol: "tcp",
				Internal: "1234",
			}},
			err: "Port name -!-@-#-$-%-^-&-*-(-)- does not contain any letters or digits",
		},
		{
			desc: "Multiple ports should be supported",
			ports: []*model.RoleRunExposedPort{
				{
					Name:     "first-port",
					Protocol: "tcp",
					Internal: "1234",
				},
				{
					Name:     "second-port",
					Protocol: "udp",
					Internal: "5678",
				},
			},
			expected: []v1.ContainerPort{
				{
					Name:          "first-port",
					ContainerPort: 1234,
					Protocol:      v1.ProtocolTCP,
				},
				{
					Name:          "second-port",
					ContainerPort: 5678,
					Protocol:      v1.ProtocolUDP,
				},
			},
		},
		{
			desc: "Port range should be supported",
			ports: []*model.RoleRunExposedPort{{
				Name:     "port-range",
				Protocol: "tcp",
				Internal: "1234-1236",
			}},
			expected: []v1.ContainerPort{
				{
					Name:          "port-range-0",
					Protocol:      v1.ProtocolTCP,
					ContainerPort: 1234,
				},
				{
					Name:          "port-range-1",
					Protocol:      v1.ProtocolTCP,
					ContainerPort: 1235,
				},
				{
					Name:          "port-range-2",
					Protocol:      v1.ProtocolTCP,
					ContainerPort: 1236,
				},
			},
		},
	}

	// TODO use golang 1.7's subtests
	for _, sample := range samples {
		role.Run.ExposedPorts = sample.ports
		actual, err := getContainerPorts(role)
		if sample.err != "" {
			assert.EqualError(err, sample.err, sample.desc)
		} else if assert.NoError(err, sample.desc) {
			assert.Equal(sample.expected, actual, sample.desc)
		}
	}
}

func TestPodGetContainerReadinessProbe(t *testing.T) {
	assert := assert.New(t)
	role := podTestLoadRole(assert)
	if role == nil {
		return
	}

	samples := []struct {
		desc     string
		probe    *model.HealthCheck
		expected *v1.Probe
		err      string
	}{
		{
			desc:     "No probe",
			probe:    nil,
			expected: nil,
		},
		{
			desc: "Port probe",
			probe: &model.HealthCheck{
				Port: 1234,
			},
			expected: &v1.Probe{
				Handler: v1.Handler{
					TCPSocket: &v1.TCPSocketAction{
						Port: intstr.FromInt(1234),
					},
				},
			},
		},
		{
			desc: "Command probe",
			probe: &model.HealthCheck{
				Command: []string{"rm", "-rf", "--no-preserve-root", "/"},
			},
			expected: &v1.Probe{
				Handler: v1.Handler{
					Exec: &v1.ExecAction{
						Command: []string{"rm", "-rf", "--no-preserve-root", "/"},
					},
				},
			},
		},
		{
			desc: "URL probe (simple)",
			probe: &model.HealthCheck{
				URL: "http://example.com/path",
			},
			expected: &v1.Probe{
				Handler: v1.Handler{
					HTTPGet: &v1.HTTPGetAction{
						Scheme: v1.URISchemeHTTP,
						Host:   "example.com",
						Port:   intstr.FromInt(80),
						Path:   "/path",
					},
				},
			},
		},
		{
			desc: "URL probe (custom port)",
			probe: &model.HealthCheck{
				URL: "https://example.com:1234/path",
			},
			expected: &v1.Probe{
				Handler: v1.Handler{
					HTTPGet: &v1.HTTPGetAction{
						Scheme: v1.URISchemeHTTPS,
						Host:   "example.com",
						Port:   intstr.FromInt(1234),
						Path:   "/path",
					},
				},
			},
		},
		{
			desc: "URL probe (Invalid scheme)",
			probe: &model.HealthCheck{
				URL: "file:///etc/shadow",
			},
			err: "Health check for myrole has unsupported URI scheme \"file\"",
		},
		{
			desc: "URL probe (query)",
			probe: &model.HealthCheck{
				URL: "http://example.com/path?query#hash",
			},
			expected: &v1.Probe{
				Handler: v1.Handler{
					HTTPGet: &v1.HTTPGetAction{
						Scheme: v1.URISchemeHTTP,
						Host:   "example.com",
						Port:   intstr.FromInt(80),
						Path:   "/path?query",
					},
				},
			},
		},
		{
			desc: "URL probe (auth)",
			probe: &model.HealthCheck{
				URL: "http://user:pass@example.com/path",
			},
			expected: &v1.Probe{
				Handler: v1.Handler{
					HTTPGet: &v1.HTTPGetAction{
						Scheme: v1.URISchemeHTTP,
						Host:   "example.com",
						Port:   intstr.FromInt(80),
						Path:   "/path",
						HTTPHeaders: []v1.HTTPHeader{
							{
								Name:  "Authorization",
								Value: base64.StdEncoding.EncodeToString([]byte("user:pass")),
							},
						},
					},
				},
			},
		},
		{
			desc: "URL probe (custom headers)",
			probe: &model.HealthCheck{
				URL:     "http://example.com/path",
				Headers: map[string]string{"x-header": "some value"},
			},
			expected: &v1.Probe{
				Handler: v1.Handler{
					HTTPGet: &v1.HTTPGetAction{
						Scheme: v1.URISchemeHTTP,
						Host:   "example.com",
						Port:   intstr.FromInt(80),
						Path:   "/path",
						HTTPHeaders: []v1.HTTPHeader{
							{
								Name:  "X-Header",
								Value: "some value",
							},
						},
					},
				},
			},
		},
		{
			desc: "URL probe (invalid URL)",
			probe: &model.HealthCheck{
				URL: "://",
			},
			err: "Invalid URL health check for myrole: parse ://: missing protocol scheme",
		},
		{
			desc: "URL probe (invalid port)",
			probe: &model.HealthCheck{
				URL: "http://example.com:port_number/",
			},
			err: "Failed to get URL port for health check for myrole: invalid host \"example.com:port_number\"",
		},
		{
			desc: "URL probe (localhost)",
			probe: &model.HealthCheck{
				URL: "http://container-ip/path",
			},
			expected: &v1.Probe{
				Handler: v1.Handler{
					HTTPGet: &v1.HTTPGetAction{
						Scheme: v1.URISchemeHTTP,
						Port:   intstr.FromInt(80),
						Path:   "/path",
					},
				},
			},
		},
	}

	// TODO use golang 1.7's subtests
	for _, sample := range samples {
		role.Run.HealthCheck = sample.probe
		actual, err := getContainerReadinessProbe(role)
		if sample.err != "" {
			assert.EqualError(err, sample.err, sample.desc)
		} else {
			assert.NoError(err, sample.desc)
			assert.Equal(sample.expected, actual, sample.desc)
		}
	}
}
