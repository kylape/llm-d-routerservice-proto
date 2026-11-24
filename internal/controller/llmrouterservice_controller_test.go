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
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	llmdv1alpha1 "github.com/llm-d/llm-d-routerservice-proto/api/v1alpha1"
	igwapi "sigs.k8s.io/gateway-api-inference-extension/api/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	testNamespace = "test"
	simImage      = "ghcr.io/llm-d/llm-d-inference-sim:v0.11.2"
	eppImage      = "ghcr.io/llm-d/llm-d-router-endpoint-picker:v1.0.0"
)

func newTestHarness(t *testing.T) (client.WithWatch, *LLMRouterServiceReconciler, *LLMRouterServiceProfileReconciler) {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, llmdv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, appsv1.AddToScheme(scheme))
	require.NoError(t, rbacv1.AddToScheme(scheme))
	require.NoError(t, igwapi.Install(scheme))
	require.NoError(t, gwapiv1.Install(scheme))

	c := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(
			&llmdv1alpha1.LLMRouterService{},
			&llmdv1alpha1.LLMRouterServiceProfile{},
			&appsv1.Deployment{},
		).Build()
	return c,
		&LLMRouterServiceReconciler{Client: c, Scheme: scheme, ModelServerImage: simImage, EPPImage: eppImage},
		&LLMRouterServiceProfileReconciler{Client: c, Scheme: scheme}
}

func requestFor(name string) reconcile.Request {
	return reconcile.Request{NamespacedName: types.NamespacedName{Namespace: testNamespace, Name: name}}
}

func assertCondition(t *testing.T, conds []metav1.Condition, condType string, wantStatus bool, wantReason string) {
	t.Helper()
	for _, cond := range conds {
		if cond.Type == condType {
			assert.Equal(t, wantStatus, cond.Status == metav1.ConditionTrue, "condition %s status", condType)
			assert.Equal(t, wantReason, cond.Reason, "condition %s reason", condType)
			return
		}
	}
	t.Fatalf("condition %s not found", condType)
}

// makeDeploymentAvailable simulates the Deployment controller marking a
// Deployment Available (envtest-style external status update).
func makeDeploymentAvailable(ctx context.Context, t *testing.T, c client.Client, name string) {
	t.Helper()
	d := &appsv1.Deployment{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: name, Namespace: testNamespace}, d))
	d.Status.AvailableReplicas = ptr.Deref(d.Spec.Replicas, 1)
	d.Status.Conditions = append(d.Status.Conditions, appsv1.DeploymentCondition{
		Type:   appsv1.DeploymentAvailable,
		Status: corev1.ConditionTrue,
	})
	require.NoError(t, c.Status().Update(ctx, d))
}

// demoService builds the demo-shaped service: model sim-qwen, decode
// replicas 2, gateway ref, profileRef.
func demoService(name, profileRef string) *llmdv1alpha1.LLMRouterService {
	return &llmdv1alpha1.LLMRouterService{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, Generation: 1},
		Spec: llmdv1alpha1.LLMRouterServiceSpec{
			Model:      llmdv1alpha1.ModelSpec{Name: "sim-qwen", URI: "hf://simulated/qwen"},
			ProfileRef: profileRef,
			Workload: llmdv1alpha1.WorkloadSpec{
				Kind:   "Deployment",
				Decode: &llmdv1alpha1.WorkloadRoleSpec{Replicas: ptr.To(int32(2))},
			},
			Routing: llmdv1alpha1.RoutingSpec{
				Mode:    "gateway",
				Gateway: &llmdv1alpha1.GatewaySpec{Refs: []llmdv1alpha1.GatewayRef{{Provider: "envoy", Name: "inference-gateway"}}},
			},
		},
	}
}

func profileWithConfig(name string, generation int64, pluginTypes ...string) *llmdv1alpha1.LLMRouterServiceProfile {
	plugins := make([]llmdv1alpha1.PluginSpec, 0, len(pluginTypes))
	for _, p := range pluginTypes {
		plugins = append(plugins, llmdv1alpha1.PluginSpec{Type: p})
	}
	return &llmdv1alpha1.LLMRouterServiceProfile{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace, Generation: generation},
		Spec: llmdv1alpha1.LLMRouterServiceProfileSpec{
			Routing: &llmdv1alpha1.RoutingProfileSpec{
				Router: &llmdv1alpha1.RouterProfileSpec{EPP: &llmdv1alpha1.EPPProfileSpec{
					Replicas: ptr.To(int32(1)),
					EndpointPicker: &llmdv1alpha1.EndpointPickerConfig{
						Plugins: plugins,
						SchedulingProfiles: []llmdv1alpha1.SchedulingProfile{{
							Name:    "default",
							Plugins: []llmdv1alpha1.SchedulingPlugin{{PluginRef: "queue-scorer"}},
						}},
					},
				}},
			},
		},
	}
}

// TestFullStackRendersAndConverges verifies the single-object
// rendering: one service renders the model-server Deployment, the EPP
// Deployment + Service + ConfigMap + RBAC, the InferencePool and the
// HTTPRoute; once both Deployments go Available, all conditions go
// True.
func TestFullStackRendersAndConverges(t *testing.T) {
	ctx := context.Background()
	c, r, _ := newTestHarness(t)

	profile := profileWithConfig("gold", 1, "queue-scorer", "prefix-cache-scorer")
	require.NoError(t, c.Create(ctx, profile))
	svc := demoService("qwen", "gold")
	require.NoError(t, c.Create(ctx, svc))

	_, err := r.Reconcile(ctx, requestFor("qwen"))
	require.NoError(t, err)

	// Model-server Deployment: replicas from the service spec, sim
	// stand-in synthesized (image + args) since the decode template is
	// unset.
	ms := &appsv1.Deployment{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "qwen-model-server", Namespace: testNamespace}, ms))
	assert.Equal(t, int32(2), *ms.Spec.Replicas)
	assert.Equal(t, map[string]string{
		"app.kubernetes.io/managed-by": "llm-d-routerservice-operator",
		"llm-d.ai/router-service":      "qwen",
		"llm-d.ai/component":           "model-server",
	}, ms.Spec.Selector.MatchLabels)
	engine := ms.Spec.Template.Spec.Containers[0]
	assert.Equal(t, simImage, engine.Image)
	assert.Equal(t, []string{"--model", "sim-qwen", "--port", "8000"}, engine.Args)

	// EPP ConfigMap: the profile's config, rendered as the wire
	// document.
	cm := &corev1.ConfigMap{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "qwen-epp-config", Namespace: testNamespace}, cm))
	assert.Contains(t, cm.Data["config.yaml"], "kind: EndpointPickerConfig")
	assert.Contains(t, cm.Data["config.yaml"], "prefix-cache-scorer")

	// EPP Deployment: deterministic wiring args, config volume.
	epp := &appsv1.Deployment{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "qwen-epp", Namespace: testNamespace}, epp))
	args := strings.Join(epp.Spec.Template.Spec.Containers[0].Args, " ")
	assert.Contains(t, args, "--pool-name qwen")
	assert.Contains(t, args, "--pool-namespace test")
	assert.Contains(t, args, "--pool-group inference.networking.k8s.io")
	assert.Contains(t, args, "--config-file /config/config.yaml")
	assert.Contains(t, args, "--grpc-port 9002")
	assert.Equal(t, eppImage, epp.Spec.Template.Spec.Containers[0].Image)
	assert.Equal(t, "qwen-epp", epp.Spec.Template.Spec.ServiceAccountName)
	assert.Equal(t, "/config", epp.Spec.Template.Spec.Containers[0].VolumeMounts[0].MountPath)

	// EPP Service: ext-proc + metrics ports.
	svcObj := &corev1.Service{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "qwen-epp", Namespace: testNamespace}, svcObj))
	assert.Len(t, svcObj.Spec.Ports, 2)

	// EPP RBAC.
	sa := &corev1.ServiceAccount{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "qwen-epp", Namespace: testNamespace}, sa))

	// InferencePool: selector derived from the model-server
	// Deployment's own selector, endpointPickerRef to the EPP service.
	pool := &igwapi.InferencePool{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "qwen", Namespace: testNamespace}, pool))
	assert.Equal(t, map[igwapi.LabelKey]igwapi.LabelValue{
		"app.kubernetes.io/managed-by": "llm-d-routerservice-operator",
		"llm-d.ai/router-service":      "qwen",
		"llm-d.ai/component":           "model-server",
	}, pool.Spec.Selector.MatchLabels)
	require.NotNil(t, pool.Spec.EndpointPickerRef)
	assert.Equal(t, "qwen-epp", string(pool.Spec.EndpointPickerRef.Name))
	assert.Equal(t, igwapi.PortNumber(EPPGRPCPort), pool.Spec.EndpointPickerRef.Port.Number)
	assert.Equal(t, []igwapi.Port{{Number: 8000}}, pool.Spec.TargetPorts)

	// HTTPRoute: parentRef from gateway.refs[0], cross-group
	// backendRef to the pool.
	route := &gwapiv1.HTTPRoute{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "qwen", Namespace: testNamespace}, route))
	require.Len(t, route.Spec.ParentRefs, 1)
	assert.Equal(t, "inference-gateway", string(route.Spec.ParentRefs[0].Name))
	require.Len(t, route.Spec.Rules, 1)
	require.Len(t, route.Spec.Rules[0].BackendRefs, 1)
	ref := route.Spec.Rules[0].BackendRefs[0].BackendRef
	assert.Equal(t, "inference.networking.k8s.io", string(*ref.Group))
	assert.Equal(t, "InferencePool", string(*ref.Kind))
	assert.Equal(t, "qwen", string(ref.Name))

	// Status: pin + hash recorded; conditions False while
	// Deployments roll.
	current := &llmdv1alpha1.LLMRouterService{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "qwen", Namespace: testNamespace}, current))
	require.NotNil(t, current.Status.ResolvedProfileRef)
	assert.Equal(t, llmdv1alpha1.ResolvedProfileRef{Name: "gold", Generation: 1}, *current.Status.ResolvedProfileRef)
	assert.NotEmpty(t, current.Status.EndpointPickerConfigHash)
	assertCondition(t, current.Status.Conditions, "ProfilesReady", true, "Ready")
	assertCondition(t, current.Status.Conditions, "WorkloadsReady", false, "Unavailable")
	assertCondition(t, current.Status.Conditions, "RoutingReady", false, "EPPUnavailable")
	assertCondition(t, current.Status.Conditions, "Ready", false, "Unavailable")

	// Simulate both Deployment controllers reporting Available.
	makeDeploymentAvailable(ctx, t, c, "qwen-model-server")
	makeDeploymentAvailable(ctx, t, c, "qwen-epp")
	_, err = r.Reconcile(ctx, requestFor("qwen"))
	require.NoError(t, err)

	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "qwen", Namespace: testNamespace}, current))
	assertCondition(t, current.Status.Conditions, "WorkloadsReady", true, "Available")
	assertCondition(t, current.Status.Conditions, "RoutingReady", true, "Wired")
	assertCondition(t, current.Status.Conditions, "Ready", true, "Ready")
	assert.Equal(t, int64(1), current.Status.ObservedGeneration)

	// Idempotency: a no-op reconcile must not rewrite status.
	before := current.ResourceVersion
	_, err = r.Reconcile(ctx, requestFor("qwen"))
	require.NoError(t, err)
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "qwen", Namespace: testNamespace}, current))
	assert.Equal(t, before, current.ResourceVersion, "no-op reconcile must not rewrite status")
}

// TestProfileDriftHoldsChildrenThenAcceptRePins verifies the
// proposal's upgrade-safety mechanic: a profile whose generation
// drifted from the pin is not applied — children stay untouched with a
// ProfileDrift condition — until the user accepts via the
// llm-d.ai/accept-profile-drift annotation, which re-pins, applies and
// removes itself.
func TestProfileDriftHoldsChildrenThenAcceptRePins(t *testing.T) {
	ctx := context.Background()
	c, r, _ := newTestHarness(t)

	profile := profileWithConfig("gold", 1, "queue-scorer")
	require.NoError(t, c.Create(ctx, profile))
	svc := demoService("qwen", "gold")
	require.NoError(t, c.Create(ctx, svc))

	_, err := r.Reconcile(ctx, requestFor("qwen"))
	require.NoError(t, err)

	cmBefore := &corev1.ConfigMap{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "qwen-epp-config", Namespace: testNamespace}, cmBefore))
	msBefore := &appsv1.Deployment{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "qwen-model-server", Namespace: testNamespace}, msBefore))

	// Mutate the profile: new plugin set, new generation.
	updated := &llmdv1alpha1.LLMRouterServiceProfile{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "gold", Namespace: testNamespace}, updated))
	updated.Spec.Routing.Router.EPP.EndpointPicker.Plugins = []llmdv1alpha1.PluginSpec{{Type: "kv-cache-utilization-scorer"}}
	updated.Generation = 2
	require.NoError(t, c.Update(ctx, updated))

	_, err = r.Reconcile(ctx, requestFor("qwen"))
	require.NoError(t, err)

	// Children untouched, drift condition set, pin unchanged.
	cmAfter := &corev1.ConfigMap{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "qwen-epp-config", Namespace: testNamespace}, cmAfter))
	assert.Equal(t, cmBefore.Data, cmAfter.Data, "drifted profile must not touch the EPP ConfigMap")
	msAfter := &appsv1.Deployment{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "qwen-model-server", Namespace: testNamespace}, msAfter))
	assert.Equal(t, msBefore.Spec, msAfter.Spec, "drifted profile must not touch children")

	current := &llmdv1alpha1.LLMRouterService{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "qwen", Namespace: testNamespace}, current))
	assertCondition(t, current.Status.Conditions, "ProfilesReady", false, "ProfileDrift")
	assertCondition(t, current.Status.Conditions, "Ready", false, "ProfileDrift")
	require.NotNil(t, current.Status.ResolvedProfileRef)
	assert.Equal(t, int64(1), current.Status.ResolvedProfileRef.Generation, "pin must stay at the old generation")
	assert.NotContains(t, current.Annotations, llmdv1alpha1.AcceptProfileDriftAnnotation)

	// Accept: set the annotation, reconcile, expect re-pin + apply.
	accepted := &llmdv1alpha1.LLMRouterService{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "qwen", Namespace: testNamespace}, accepted))
	accepted.Annotations = map[string]string{llmdv1alpha1.AcceptProfileDriftAnnotation: "true"}
	require.NoError(t, c.Update(ctx, accepted))

	_, err = r.Reconcile(ctx, requestFor("qwen"))
	require.NoError(t, err)

	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "qwen-epp-config", Namespace: testNamespace}, cmAfter))
	assert.Contains(t, cmAfter.Data["config.yaml"], "kv-cache-utilization-scorer", "accepted drift must apply the new profile content")

	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "qwen", Namespace: testNamespace}, current))
	require.NotNil(t, current.Status.ResolvedProfileRef)
	assert.Equal(t, llmdv1alpha1.ResolvedProfileRef{Name: "gold", Generation: 2}, *current.Status.ResolvedProfileRef)
	assertCondition(t, current.Status.Conditions, "ProfilesReady", true, "Ready")
	assert.NotContains(t, current.Annotations, llmdv1alpha1.AcceptProfileDriftAnnotation,
		"the accept annotation is one-shot and must be removed after acceptance")
}

// TestProfileRefChangeRePins verifies that changing spec.profileRef is
// an explicit opt-in: the new profile pins at its current generation
// without a drift hold.
func TestProfileRefChangeRePins(t *testing.T) {
	ctx := context.Background()
	c, r, _ := newTestHarness(t)

	gold := profileWithConfig("gold", 1, "queue-scorer")
	require.NoError(t, c.Create(ctx, gold))
	silver := profileWithConfig("silver", 7, "prefix-cache-scorer")
	require.NoError(t, c.Create(ctx, silver))
	svc := demoService("qwen", "gold")
	require.NoError(t, c.Create(ctx, svc))

	_, err := r.Reconcile(ctx, requestFor("qwen"))
	require.NoError(t, err)

	repointed := &llmdv1alpha1.LLMRouterService{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "qwen", Namespace: testNamespace}, repointed))
	repointed.Spec.ProfileRef = "silver"
	require.NoError(t, c.Update(ctx, repointed))

	_, err = r.Reconcile(ctx, requestFor("qwen"))
	require.NoError(t, err)

	cm := &corev1.ConfigMap{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "qwen-epp-config", Namespace: testNamespace}, cm))
	assert.Contains(t, cm.Data["config.yaml"], "prefix-cache-scorer")

	current := &llmdv1alpha1.LLMRouterService{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "qwen", Namespace: testNamespace}, current))
	assert.Equal(t, llmdv1alpha1.ResolvedProfileRef{Name: "silver", Generation: 7}, *current.Status.ResolvedProfileRef)
	assertCondition(t, current.Status.Conditions, "ProfilesReady", true, "Ready")
}

// TestProfileNotFoundHolds verifies the missing-profile hold: no
// children render, ProfilesReady=False/ProfileNotFound.
func TestProfileNotFoundHolds(t *testing.T) {
	ctx := context.Background()
	c, r, _ := newTestHarness(t)

	require.NoError(t, c.Create(ctx, demoService("qwen", "does-not-exist")))

	_, err := r.Reconcile(ctx, requestFor("qwen"))
	require.NoError(t, err)

	ms := &appsv1.Deployment{}
	err = c.Get(ctx, types.NamespacedName{Name: "qwen-model-server", Namespace: testNamespace}, ms)
	assert.Error(t, err, "no children must render while the profile is missing")

	current := &llmdv1alpha1.LLMRouterService{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "qwen", Namespace: testNamespace}, current))
	assertCondition(t, current.Status.Conditions, "ProfilesReady", false, "ProfileNotFound")
	assertCondition(t, current.Status.Conditions, "Ready", false, "ProfileNotFound")
	assert.Nil(t, current.Status.ResolvedProfileRef)
}

// TestNoProfileRendersFromServiceSpec verifies the profile-less path.
func TestNoProfileRendersFromServiceSpec(t *testing.T) {
	ctx := context.Background()
	c, r, _ := newTestHarness(t)

	require.NoError(t, c.Create(ctx, demoService("qwen", "")))

	_, err := r.Reconcile(ctx, requestFor("qwen"))
	require.NoError(t, err)

	cm := &corev1.ConfigMap{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "qwen-epp-config", Namespace: testNamespace}, cm))
	assert.Contains(t, cm.Data["config.yaml"], "prefix-cache-scorer", "controller default config must apply")

	current := &llmdv1alpha1.LLMRouterService{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "qwen", Namespace: testNamespace}, current))
	assertCondition(t, current.Status.Conditions, "ProfilesReady", true, "NoProfile")
	assert.Nil(t, current.Status.ResolvedProfileRef)
	assert.NotEmpty(t, current.Status.EndpointPickerConfigHash)
}

// TestUnsupportedTopologyRejection verifies that disaggregated and
// multi-node topologies are rejected with WorkloadsReady=False/
// UnsupportedTopology and no children.
func TestUnsupportedTopologyRejection(t *testing.T) {
	ctx := context.Background()
	c, r, _ := newTestHarness(t)

	cases := []struct {
		name   string
		mutate func(*llmdv1alpha1.LLMRouterServiceSpec)
	}{
		{
			name: "prefill selects P/D",
			mutate: func(s *llmdv1alpha1.LLMRouterServiceSpec) {
				s.Workload.Prefill = &llmdv1alpha1.WorkloadRoleSpec{}
			},
		},
		{
			name: "encode selects E/P/D",
			mutate: func(s *llmdv1alpha1.LLMRouterServiceSpec) {
				s.Workload.Encode = &llmdv1alpha1.WorkloadRoleSpec{}
			},
		},
		{
			name: "worker selects multi-node",
			mutate: func(s *llmdv1alpha1.LLMRouterServiceSpec) {
				s.Workload.Worker = &llmdv1alpha1.WorkloadRoleSpec{}
			},
		},
		{
			name: "DisaggregatedSet kind",
			mutate: func(s *llmdv1alpha1.LLMRouterServiceSpec) {
				s.Workload.Kind = "DisaggregatedSet"
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := demoService("qwen", "")
			tc.mutate(&svc.Spec)
			require.NoError(t, c.Create(ctx, svc))

			_, err := r.Reconcile(ctx, requestFor("qwen"))
			require.NoError(t, err)

			ms := &appsv1.Deployment{}
			assert.Error(t, c.Get(ctx, types.NamespacedName{Name: "qwen-model-server", Namespace: testNamespace}, ms),
				"no children must render for an unsupported topology")

			current := &llmdv1alpha1.LLMRouterService{}
			require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "qwen", Namespace: testNamespace}, current))
			assertCondition(t, current.Status.Conditions, "WorkloadsReady", false, "UnsupportedTopology")
			assertCondition(t, current.Status.Conditions, "RoutingReady", false, "UnsupportedTopology")
			assertCondition(t, current.Status.Conditions, "Ready", false, "UnsupportedTopology")

			// Cleanup for the next case.
			require.NoError(t, c.Delete(ctx, current))
			require.NoError(t, c.DeleteAllOf(ctx, &llmdv1alpha1.LLMRouterService{}, client.InNamespace(testNamespace)))
		})
	}
}

// TestStandaloneModeRejected verifies mode:standalone renders the
// workload but holds the routing stack with RoutingReady=False/
// UnsupportedMode.
func TestStandaloneModeRejected(t *testing.T) {
	ctx := context.Background()
	c, r, _ := newTestHarness(t)

	svc := demoService("qwen", "")
	svc.Spec.Routing.Mode = "standalone"
	require.NoError(t, c.Create(ctx, svc))

	_, err := r.Reconcile(ctx, requestFor("qwen"))
	require.NoError(t, err)

	ms := &appsv1.Deployment{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "qwen-model-server", Namespace: testNamespace}, ms),
		"the workload still renders in standalone mode")

	epp := &appsv1.Deployment{}
	assert.Error(t, c.Get(ctx, types.NamespacedName{Name: "qwen-epp", Namespace: testNamespace}, epp),
		"routing children must not render in standalone mode")

	current := &llmdv1alpha1.LLMRouterService{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "qwen", Namespace: testNamespace}, current))
	assertCondition(t, current.Status.Conditions, "RoutingReady", false, "UnsupportedMode")
	// Ready surfaces the first failing sub-condition: the workload
	// Deployment exists but is not yet Available in the fake client.
	ready := findCondition(t, current.Status.Conditions, "Ready")
	assert.Equal(t, metav1.ConditionFalse, ready.Status)
}

func findCondition(t *testing.T, conds []metav1.Condition, condType string) metav1.Condition {
	t.Helper()
	for _, cond := range conds {
		if cond.Type == condType {
			return cond
		}
	}
	t.Fatalf("condition %s not found", condType)
	return metav1.Condition{}
}

// TestDisaggSidecarInjection verifies the proposal's controller
// behavior: decode.disaggSidecar is injected into the decode workload.
func TestDisaggSidecarInjection(t *testing.T) {
	ctx := context.Background()
	c, r, _ := newTestHarness(t)

	svc := demoService("qwen", "")
	svc.Spec.Workload.Decode.DisaggSidecar = &llmdv1alpha1.DisaggSidecarSpec{
		Container: corev1.Container{Image: "ghcr.io/llm-d/llm-d-router-disagg-sidecar:v0.1.0"},
	}
	require.NoError(t, c.Create(ctx, svc))

	_, err := r.Reconcile(ctx, requestFor("qwen"))
	require.NoError(t, err)

	ms := &appsv1.Deployment{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "qwen-model-server", Namespace: testNamespace}, ms))
	require.Len(t, ms.Spec.Template.Spec.Containers, 2)
	sidecar := ms.Spec.Template.Spec.Containers[1]
	assert.Equal(t, "disagg-sidecar", sidecar.Name)
	assert.Equal(t, "ghcr.io/llm-d/llm-d-router-disagg-sidecar:v0.1.0", sidecar.Image)
}

// TestTargetPortsAndEngineTemplate verifies the user-settable pool
// target ports and the engine template passthrough.
func TestTargetPortsAndEngineTemplate(t *testing.T) {
	ctx := context.Background()
	c, r, _ := newTestHarness(t)

	svc := demoService("qwen", "")
	svc.Spec.Routing.InferencePool = &llmdv1alpha1.InferencePoolSettings{Spec: llmdv1alpha1.InferencePoolOverride{
		TargetPorts: []llmdv1alpha1.TargetPort{{Name: "http", Number: 8100}},
	}}
	svc.Spec.Workload.Decode.Template = &llmdv1alpha1.RoleTemplateSpec{
		Containers: []corev1.Container{{
			Name:  "vllm",
			Image: "vllm/vllm-openai:v0.11.0",
			Args:  []string{"--model", "Qwen/Qwen2.5-32B-Instruct"},
		}},
	}
	require.NoError(t, c.Create(ctx, svc))

	_, err := r.Reconcile(ctx, requestFor("qwen"))
	require.NoError(t, err)

	pool := &igwapi.InferencePool{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "qwen", Namespace: testNamespace}, pool))
	assert.Equal(t, []igwapi.Port{{Number: 8100}}, pool.Spec.TargetPorts)

	ms := &appsv1.Deployment{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "qwen-model-server", Namespace: testNamespace}, ms))
	engine := ms.Spec.Template.Spec.Containers[0]
	assert.Equal(t, "vllm", engine.Name)
	assert.Equal(t, "vllm/vllm-openai:v0.11.0", engine.Image)
	assert.Equal(t, []string{"--model", "Qwen/Qwen2.5-32B-Instruct"}, engine.Args)
}

// TestNoGatewayRefHoldsRouteOnly verifies that a missing gateway ref
// renders the rest of the routing stack but not the HTTPRoute.
func TestNoGatewayRefHoldsRouteOnly(t *testing.T) {
	ctx := context.Background()
	c, r, _ := newTestHarness(t)

	svc := demoService("qwen", "")
	svc.Spec.Routing.Gateway = nil
	require.NoError(t, c.Create(ctx, svc))

	_, err := r.Reconcile(ctx, requestFor("qwen"))
	require.NoError(t, err)

	pool := &igwapi.InferencePool{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "qwen", Namespace: testNamespace}, pool),
		"the pool still renders without a gateway ref")

	route := &gwapiv1.HTTPRoute{}
	assert.Error(t, c.Get(ctx, types.NamespacedName{Name: "qwen", Namespace: testNamespace}, route),
		"no HTTPRoute without a gateway ref")

	current := &llmdv1alpha1.LLMRouterService{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "qwen", Namespace: testNamespace}, current))
	assertCondition(t, current.Status.Conditions, "RoutingReady", false, "NoGatewayRef")
}

// TestConfigHashMatchesRenderedDocument verifies
// status.endpointPickerConfigHash is the sha256 of the exact ConfigMap
// document.
func TestConfigHashMatchesRenderedDocument(t *testing.T) {
	ctx := context.Background()
	c, r, _ := newTestHarness(t)

	require.NoError(t, c.Create(ctx, demoService("qwen", "")))

	_, err := r.Reconcile(ctx, requestFor("qwen"))
	require.NoError(t, err)

	cm := &corev1.ConfigMap{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "qwen-epp-config", Namespace: testNamespace}, cm))
	_, hash, err := eppConfig(defaultEPPConfig())
	require.NoError(t, err)

	current := &llmdv1alpha1.LLMRouterService{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "qwen", Namespace: testNamespace}, current))
	assert.Equal(t, hash, current.Status.EndpointPickerConfigHash)
}

// TestServiceSpecChangeApplies verifies service-owned spec changes
// always re-render (the pin only guards profile content).
func TestServiceSpecChangeApplies(t *testing.T) {
	ctx := context.Background()
	c, r, _ := newTestHarness(t)

	profile := profileWithConfig("gold", 1, "queue-scorer")
	require.NoError(t, c.Create(ctx, profile))
	require.NoError(t, c.Create(ctx, demoService("qwen", "gold")))

	_, err := r.Reconcile(ctx, requestFor("qwen"))
	require.NoError(t, err)

	updated := &llmdv1alpha1.LLMRouterService{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "qwen", Namespace: testNamespace}, updated))
	updated.Spec.Workload.Decode.Replicas = ptr.To(int32(4))
	require.NoError(t, c.Update(ctx, updated))

	_, err = r.Reconcile(ctx, requestFor("qwen"))
	require.NoError(t, err)

	ms := &appsv1.Deployment{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "qwen-model-server", Namespace: testNamespace}, ms))
	assert.Equal(t, int32(4), *ms.Spec.Replicas)

	current := &llmdv1alpha1.LLMRouterService{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "qwen", Namespace: testNamespace}, current))
	assertCondition(t, current.Status.Conditions, "ProfilesReady", true, "Ready")
}

// TestProfilesToServicesMap verifies the profile watch mapping.
func TestProfilesToServicesMap(t *testing.T) {
	ctx := context.Background()
	c, r, _ := newTestHarness(t)

	require.NoError(t, c.Create(ctx, demoService("a", "gold")))
	require.NoError(t, c.Create(ctx, demoService("b", "silver")))
	require.NoError(t, c.Create(ctx, demoService("c", "")))

	reqs := r.profilesToServices(ctx, &llmdv1alpha1.LLMRouterServiceProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "gold", Namespace: testNamespace},
	})
	require.Len(t, reqs, 1)
	assert.Equal(t, "a", reqs[0].Name)
}
