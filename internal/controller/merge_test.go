/*
Copyright 2026 The llm-d Authors.

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

package controller

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	llmdv1alpha1 "github.com/llm-d/llm-d-routerservice-proto/api/v1alpha1"
)

func ptrInt32(i int32) *int32 { return ptr.To(i) }

func serviceSpec() *llmdv1alpha1.LLMRouterServiceSpec {
	return &llmdv1alpha1.LLMRouterServiceSpec{Model: llmdv1alpha1.ModelSpec{Name: "m"}}
}

func profileSpec() *llmdv1alpha1.LLMRouterServiceProfileSpec {
	return &llmdv1alpha1.LLMRouterServiceProfileSpec{}
}

// testContainers builds role template containers from image names.
func testContainers(images ...string) []corev1.Container {
	containers := make([]corev1.Container, 0, len(images))
	for _, image := range images {
		containers = append(containers, corev1.Container{Image: image})
	}
	return containers
}

// The table below exercises the merge's precedence rules: the service
// wins everywhere it sets a field; the profile fills unset fields;
// structs merge field-wise; lists replace wholesale.
func TestMergeSpecPrecedence(t *testing.T) {
	tests := []struct {
		name    string
		service func(*llmdv1alpha1.LLMRouterServiceSpec)
		profile func(*llmdv1alpha1.LLMRouterServiceProfileSpec)
		assert  func(*testing.T, *MergedSpec)
	}{
		{
			name:    "no profile passes the service through",
			service: func(s *llmdv1alpha1.LLMRouterServiceSpec) {},
			profile: nil,
			assert: func(t *testing.T, m *MergedSpec) {
				assert.Equal(t, llmdv1alpha1.WorkloadKind(""), m.Workload.Kind)
				assert.Nil(t, m.Routing.EPPConfig)
				assert.Nil(t, m.Routing.EPPReplicas)
			},
		},
		{
			name: "profile workload kind fills an unset service kind",
			profile: func(p *llmdv1alpha1.LLMRouterServiceProfileSpec) {
				p.Workload = &llmdv1alpha1.WorkloadSpec{Kind: "DisaggregatedSet"}
			},
			assert: func(t *testing.T, m *MergedSpec) {
				assert.Equal(t, llmdv1alpha1.WorkloadKind("DisaggregatedSet"), m.Workload.Kind)
			},
		},
		{
			name: "service workload kind wins over the profile",
			service: func(s *llmdv1alpha1.LLMRouterServiceSpec) {
				s.Workload.Kind = "Deployment"
			},
			profile: func(p *llmdv1alpha1.LLMRouterServiceProfileSpec) {
				p.Workload = &llmdv1alpha1.WorkloadSpec{Kind: "LeaderWorkerSet"}
			},
			assert: func(t *testing.T, m *MergedSpec) {
				assert.Equal(t, llmdv1alpha1.WorkloadKind("Deployment"), m.Workload.Kind)
			},
		},
		{
			name: "service replicas win",
			service: func(s *llmdv1alpha1.LLMRouterServiceSpec) {
				s.Workload.Decode = &llmdv1alpha1.WorkloadRoleSpec{Replicas: ptrInt32(3)}
			},
			profile: func(p *llmdv1alpha1.LLMRouterServiceProfileSpec) {
				p.Workload = &llmdv1alpha1.WorkloadSpec{Decode: &llmdv1alpha1.WorkloadRoleSpec{Replicas: ptrInt32(2)}}
			},
			assert: func(t *testing.T, m *MergedSpec) {
				require.NotNil(t, m.Workload.Decode)
				assert.Equal(t, int32(3), *m.Workload.Decode.Replicas)
			},
		},
		{
			name: "profile replicas fill an unset service field",
			service: func(s *llmdv1alpha1.LLMRouterServiceSpec) {
				s.Workload.Decode = &llmdv1alpha1.WorkloadRoleSpec{} // role present, replicas unset
			},
			profile: func(p *llmdv1alpha1.LLMRouterServiceProfileSpec) {
				p.Workload = &llmdv1alpha1.WorkloadSpec{Decode: &llmdv1alpha1.WorkloadRoleSpec{Replicas: ptrInt32(2)}}
			},
			assert: func(t *testing.T, m *MergedSpec) {
				require.NotNil(t, m.Workload.Decode)
				assert.Equal(t, int32(2), *m.Workload.Decode.Replicas)
			},
		},
		{
			name: "service template containers replace the profile's",
			service: func(s *llmdv1alpha1.LLMRouterServiceSpec) {
				s.Workload.Decode = &llmdv1alpha1.WorkloadRoleSpec{
					Template: &llmdv1alpha1.RoleTemplateSpec{Containers: testContainers("vllm:1")},
				}
			},
			profile: func(p *llmdv1alpha1.LLMRouterServiceProfileSpec) {
				p.Workload = &llmdv1alpha1.WorkloadSpec{Decode: &llmdv1alpha1.WorkloadRoleSpec{
					Template: &llmdv1alpha1.RoleTemplateSpec{Containers: testContainers("sim:2")},
				}}
			},
			assert: func(t *testing.T, m *MergedSpec) {
				require.NotNil(t, m.Workload.Decode)
				assert.Equal(t, "vllm:1", m.Workload.Decode.Template.Containers[0].Image)
			},
		},
		{
			name: "parallelism merges field-wise",
			service: func(s *llmdv1alpha1.LLMRouterServiceSpec) {
				s.Workload.Decode = &llmdv1alpha1.WorkloadRoleSpec{Parallelism: &llmdv1alpha1.ParallelismSpec{TP: ptrInt32(2)}}
			},
			profile: func(p *llmdv1alpha1.LLMRouterServiceProfileSpec) {
				p.Workload = &llmdv1alpha1.WorkloadSpec{Decode: &llmdv1alpha1.WorkloadRoleSpec{
					Parallelism: &llmdv1alpha1.ParallelismSpec{TP: ptrInt32(4), PP: ptrInt32(1)},
				}}
			},
			assert: func(t *testing.T, m *MergedSpec) {
				par := m.Workload.Decode.Parallelism
				require.NotNil(t, par)
				assert.Equal(t, int32(2), *par.TP) // service wins
				assert.Equal(t, int32(1), *par.PP) // profile fills
				assert.Nil(t, par.DP)
			},
		},
		{
			name: "service gateway refs win, profile refs fill",
			service: func(s *llmdv1alpha1.LLMRouterServiceSpec) {
				s.Routing.Gateway = &llmdv1alpha1.GatewaySpec{Refs: []llmdv1alpha1.GatewayRef{{Name: "g1"}}}
			},
			profile: func(p *llmdv1alpha1.LLMRouterServiceProfileSpec) {
				p.Routing = &llmdv1alpha1.RoutingProfileSpec{
					Gateway: &llmdv1alpha1.GatewaySpec{Refs: []llmdv1alpha1.GatewayRef{{Name: "g2"}}},
				}
			},
			assert: func(t *testing.T, m *MergedSpec) {
				require.Len(t, m.Routing.GatewayRefs, 1)
				assert.Equal(t, "g1", m.Routing.GatewayRefs[0].Name)
			},
		},
		{
			name: "profile gateway refs fill an empty service list",
			profile: func(p *llmdv1alpha1.LLMRouterServiceProfileSpec) {
				p.Routing = &llmdv1alpha1.RoutingProfileSpec{
					Gateway: &llmdv1alpha1.GatewaySpec{Refs: []llmdv1alpha1.GatewayRef{{Name: "g2"}}},
				}
			},
			assert: func(t *testing.T, m *MergedSpec) {
				require.Len(t, m.Routing.GatewayRefs, 1)
				assert.Equal(t, "g2", m.Routing.GatewayRefs[0].Name)
			},
		},
		{
			name: "service routing mode wins; profile mode fills",
			service: func(s *llmdv1alpha1.LLMRouterServiceSpec) {
				s.Routing.Mode = "gateway"
			},
			profile: func(p *llmdv1alpha1.LLMRouterServiceProfileSpec) {
				p.Routing = &llmdv1alpha1.RoutingProfileSpec{Mode: "standalone"}
			},
			assert: func(t *testing.T, m *MergedSpec) {
				assert.Equal(t, llmdv1alpha1.RoutingMode("gateway"), m.Routing.Mode)
			},
		},
		{
			name: "profile routing mode fills",
			profile: func(p *llmdv1alpha1.LLMRouterServiceProfileSpec) {
				p.Routing = &llmdv1alpha1.RoutingProfileSpec{Mode: "standalone"}
			},
			assert: func(t *testing.T, m *MergedSpec) {
				assert.Equal(t, llmdv1alpha1.RoutingMode("standalone"), m.Routing.Mode)
			},
		},
		{
			name: "profile epp replicas fill",
			profile: func(p *llmdv1alpha1.LLMRouterServiceProfileSpec) {
				p.Routing = &llmdv1alpha1.RoutingProfileSpec{
					Router: &llmdv1alpha1.RouterProfileSpec{EPP: &llmdv1alpha1.EPPProfileSpec{Replicas: ptrInt32(3)}},
				}
			},
			assert: func(t *testing.T, m *MergedSpec) {
				require.NotNil(t, m.Routing.EPPReplicas)
				assert.Equal(t, int32(3), *m.Routing.EPPReplicas)
			},
		},
		{
			name: "epp config merges field-wise: profile plugins plus service scheduling profiles",
			service: func(s *llmdv1alpha1.LLMRouterServiceSpec) {
				s.Routing.Router = &llmdv1alpha1.RouterSpec{EPP: &llmdv1alpha1.EPPSpec{
					EndpointPicker: &llmdv1alpha1.EndpointPickerSettings{Inline: &llmdv1alpha1.EndpointPickerConfig{
						SchedulingProfiles: []llmdv1alpha1.SchedulingProfile{{Name: "svc"}},
					}},
				}}
			},
			profile: func(p *llmdv1alpha1.LLMRouterServiceProfileSpec) {
				p.Routing = &llmdv1alpha1.RoutingProfileSpec{
					Router: &llmdv1alpha1.RouterProfileSpec{EPP: &llmdv1alpha1.EPPProfileSpec{
						EndpointPicker: &llmdv1alpha1.EndpointPickerConfig{
							Plugins:            []llmdv1alpha1.PluginSpec{{Type: "queue-scorer"}},
							SchedulingProfiles: []llmdv1alpha1.SchedulingProfile{{Name: "profile"}},
						},
					}},
				}
			},
			assert: func(t *testing.T, m *MergedSpec) {
				require.NotNil(t, m.Routing.EPPConfig)
				assert.Equal(t, "queue-scorer", m.Routing.EPPConfig.Plugins[0].Type)   // from profile
				assert.Equal(t, "svc", m.Routing.EPPConfig.SchedulingProfiles[0].Name) // from service
			},
		},
		{
			name: "epp config: service plugins replace the profile's",
			service: func(s *llmdv1alpha1.LLMRouterServiceSpec) {
				s.Routing.Router = &llmdv1alpha1.RouterSpec{EPP: &llmdv1alpha1.EPPSpec{
					EndpointPicker: &llmdv1alpha1.EndpointPickerSettings{Inline: &llmdv1alpha1.EndpointPickerConfig{
						Plugins: []llmdv1alpha1.PluginSpec{{Type: "svc-scorer"}},
					}},
				}}
			},
			profile: func(p *llmdv1alpha1.LLMRouterServiceProfileSpec) {
				p.Routing = &llmdv1alpha1.RoutingProfileSpec{
					Router: &llmdv1alpha1.RouterProfileSpec{EPP: &llmdv1alpha1.EPPProfileSpec{
						EndpointPicker: &llmdv1alpha1.EndpointPickerConfig{
							Plugins:            []llmdv1alpha1.PluginSpec{{Type: "profile-scorer"}},
							SchedulingProfiles: []llmdv1alpha1.SchedulingProfile{{Name: "profile"}},
						},
					}},
				}
			},
			assert: func(t *testing.T, m *MergedSpec) {
				require.NotNil(t, m.Routing.EPPConfig)
				assert.Equal(t, "svc-scorer", m.Routing.EPPConfig.Plugins[0].Type)
				assert.Equal(t, "profile", m.Routing.EPPConfig.SchedulingProfiles[0].Name)
			},
		},
		{
			name: "profile epp config adopted when the service sets none",
			profile: func(p *llmdv1alpha1.LLMRouterServiceProfileSpec) {
				p.Routing = &llmdv1alpha1.RoutingProfileSpec{
					Router: &llmdv1alpha1.RouterProfileSpec{EPP: &llmdv1alpha1.EPPProfileSpec{
						EndpointPicker: &llmdv1alpha1.EndpointPickerConfig{
							Plugins: []llmdv1alpha1.PluginSpec{{Type: "queue-scorer"}},
						},
					}},
				}
			},
			assert: func(t *testing.T, m *MergedSpec) {
				require.NotNil(t, m.Routing.EPPConfig)
				assert.Equal(t, "queue-scorer", m.Routing.EPPConfig.Plugins[0].Type)
			},
		},
		{
			name: "service target ports win; profile ports fill",
			service: func(s *llmdv1alpha1.LLMRouterServiceSpec) {
				s.Routing.InferencePool = &llmdv1alpha1.InferencePoolSettings{Spec: llmdv1alpha1.InferencePoolOverride{
					TargetPorts: []llmdv1alpha1.TargetPort{{Number: 9000}},
				}}
			},
			profile: func(p *llmdv1alpha1.LLMRouterServiceProfileSpec) {
				p.Routing = &llmdv1alpha1.RoutingProfileSpec{
					InferencePool: &llmdv1alpha1.InferencePoolSettings{Spec: llmdv1alpha1.InferencePoolOverride{
						TargetPorts: []llmdv1alpha1.TargetPort{{Number: 8001}},
					}},
				}
			},
			assert: func(t *testing.T, m *MergedSpec) {
				require.Len(t, m.Routing.PoolTargetPorts, 1)
				assert.Equal(t, int32(9000), m.Routing.PoolTargetPorts[0].Number)
			},
		},
		{
			name: "service hostnames win over the profile's",
			service: func(s *llmdv1alpha1.LLMRouterServiceSpec) {
				s.Routing.HTTPRoute = &llmdv1alpha1.HTTPRouteSettings{Hostnames: []string{"a.example.com"}}
			},
			profile: func(p *llmdv1alpha1.LLMRouterServiceProfileSpec) {
				p.Routing = &llmdv1alpha1.RoutingProfileSpec{
					HTTPRoute: &llmdv1alpha1.HTTPRouteSettings{Hostnames: []string{"b.example.com"}},
				}
			},
			assert: func(t *testing.T, m *MergedSpec) {
				assert.Equal(t, []string{"a.example.com"}, m.Routing.Hostnames)
			},
		},
		{
			name: "profile decode role adopted wholesale by a service without one",
			profile: func(p *llmdv1alpha1.LLMRouterServiceProfileSpec) {
				p.Workload = &llmdv1alpha1.WorkloadSpec{Decode: &llmdv1alpha1.WorkloadRoleSpec{
					Replicas:      ptrInt32(2),
					DisaggSidecar: &llmdv1alpha1.DisaggSidecarSpec{},
				}}
			},
			assert: func(t *testing.T, m *MergedSpec) {
				require.NotNil(t, m.Workload.Decode)
				assert.Equal(t, int32(2), *m.Workload.Decode.Replicas)
				assert.NotNil(t, m.Workload.Decode.DisaggSidecar)
			},
		},
		{
			name: "service prefill is preserved alongside profile decode defaults",
			service: func(s *llmdv1alpha1.LLMRouterServiceSpec) {
				s.Workload.Prefill = &llmdv1alpha1.WorkloadRoleSpec{Replicas: ptrInt32(4)}
			},
			profile: func(p *llmdv1alpha1.LLMRouterServiceProfileSpec) {
				p.Workload = &llmdv1alpha1.WorkloadSpec{Decode: &llmdv1alpha1.WorkloadRoleSpec{Replicas: ptrInt32(1)}}
			},
			assert: func(t *testing.T, m *MergedSpec) {
				require.NotNil(t, m.Workload.Prefill)
				assert.Equal(t, int32(4), *m.Workload.Prefill.Replicas)
				require.NotNil(t, m.Workload.Decode)
				assert.Equal(t, int32(1), *m.Workload.Decode.Replicas)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := serviceSpec()
			if tt.service != nil {
				tt.service(service)
			}
			var profile *llmdv1alpha1.LLMRouterServiceProfileSpec
			if tt.profile != nil {
				profile = profileSpec()
				tt.profile(profile)
			}
			merged := MergeSpec(service, profile)
			tt.assert(t, merged)
		})
	}
}

// TestMergeSpecDoesNotAliasInputs guards the merge's copy discipline:
// mutating the merged result must never leak into the inputs.
func TestMergeSpecDoesNotAliasInputs(t *testing.T) {
	service := serviceSpec()
	profile := profileSpec()
	profile.Workload = &llmdv1alpha1.WorkloadSpec{Decode: &llmdv1alpha1.WorkloadRoleSpec{Replicas: ptrInt32(2)}}
	profile.Routing = &llmdv1alpha1.RoutingProfileSpec{
		Router: &llmdv1alpha1.RouterProfileSpec{EPP: &llmdv1alpha1.EPPProfileSpec{
			EndpointPicker: &llmdv1alpha1.EndpointPickerConfig{
				Plugins: []llmdv1alpha1.PluginSpec{{Type: "queue-scorer"}},
			},
		}},
	}

	merged := MergeSpec(service, profile)
	require.NotNil(t, merged.Workload.Decode)
	require.NotNil(t, merged.Routing.EPPConfig)
	*merged.Workload.Decode.Replicas = 9
	merged.Routing.EPPConfig.Plugins[0].Type = "mutated"

	assert.Equal(t, int32(2), *profile.Workload.Decode.Replicas, "profile spec must not be mutated")
	assert.Equal(t, "queue-scorer", profile.Routing.Router.EPP.EndpointPicker.Plugins[0].Type)
}
