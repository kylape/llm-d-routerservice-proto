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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AcceptProfileDriftAnnotation, when set to "true" on an
// LLMRouterService whose referenced profile has drifted from the
// pinned generation, re-pins the service to the profile's current
// generation and applies it. The controller removes the annotation
// once the drift has been accepted, so acceptance is explicit per
// drift event.
const AcceptProfileDriftAnnotation = "llm-d.ai/accept-profile-drift"

// WorkloadKind selects the Kubernetes workload controller that runs
// the model server. The proposal's taxonomy keeps workload.kind
// (Deployment, DisaggregatedSet, LeaderWorkerSet) independent from the
// request-phase roles (encode/prefill/decode) and the optional
// multi-node worker group.
// +kubebuilder:validation:Enum=Deployment;DisaggregatedSet;LeaderWorkerSet
type WorkloadKind string

// RoutingMode selects how requests reach the model servers: through a
// Gateway API Gateway (gateway) or through a controller-rendered proxy
// (standalone).
// +kubebuilder:validation:Enum=gateway;standalone
type RoutingMode string

// GatewayProvider names the Gateway implementation a gateway ref
// points at.
// +kubebuilder:validation:Enum=envoy;ai-praxis
type GatewayProvider string

// ModelSpec identifies the served model. v0 implements {uri, name}
// only; lora, confidential and storageInitializer are future work
// (see the README).
type ModelSpec struct {
	// URI of the model, e.g. hf://Qwen/Qwen2.5-32B-Instruct. v0 does
	// not download models; the demo stands in the model server with
	// llm-d-inference-sim.
	// +optional
	URI string `json:"uri,omitempty"`

	// Name is the served model name, passed to the model server (and to
	// llm-d-inference-sim as --model).
	// +required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// LLMRouterServiceSpec defines the desired state of
// LLMRouterService: one user-facing inference service. It owns model
// identity and service-specific overrides; an optional profileRef
// contributes reusable defaults underneath (the service wins
// everywhere).
type LLMRouterServiceSpec struct {
	// Model identity, owned exclusively by the service.
	// +required
	Model ModelSpec `json:"model"`

	// ProfileRef names the LLMRouterServiceProfile whose defaults are
	// merged under this spec. Optional: without it the service renders
	// from its own spec plus controller defaults. The referenced
	// profile is pinned per spec.profileRef changes; see
	// status.resolvedProfileRef.
	// +optional
	// +kubebuilder:validation:MinLength=1
	ProfileRef string `json:"profileRef,omitempty"`

	// Workload is the model-server domain.
	// +optional
	Workload WorkloadSpec `json:"workload,omitempty"`

	// Routing is the llm-d-router domain: EPP, InferencePool and
	// Gateway API wiring.
	// +optional
	Routing RoutingSpec `json:"routing,omitempty"`
}

// WorkloadSpec selects the workload controller and declares the
// serving roles. The presence of prefill/encode/worker selects
// disaggregated or multi-node topologies; v0 implements decode-only
// Deployments and rejects the rest (see the README).
type WorkloadSpec struct {
	// Kind selects the Kubernetes workload controller. Defaults to
	// Deployment.
	// +optional
	// +kubebuilder:default=Deployment
	Kind WorkloadKind `json:"kind,omitempty"`

	// Prefill role. Setting it selects a disaggregated (P/D or E/P/D)
	// topology, which v0 rejects.
	// +optional
	Prefill *WorkloadRoleSpec `json:"prefill,omitempty"`

	// Decode role. The only role v0 renders.
	// +optional
	Decode *WorkloadRoleSpec `json:"decode,omitempty"`

	// Encode role. Setting it selects an E/P/D topology, which v0
	// rejects.
	// +optional
	Encode *WorkloadRoleSpec `json:"encode,omitempty"`

	// Worker is an optional multi-node worker group, orthogonal to the
	// request phases. v0 rejects it.
	// +optional
	Worker *WorkloadRoleSpec `json:"worker,omitempty"`
}

// WorkloadRoleSpec describes one serving role. The proposal's
// parallelism field names (tp/pp/dp) are kept verbatim.
type WorkloadRoleSpec struct {
	// Replicas is the number of pods for this role. Defaults to 1.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Replicas *int32 `json:"replicas,omitempty"`

	// Template is the role's pod template. v0 keeps only containers;
	// the first container is the model-server engine.
	// +optional
	Template *RoleTemplateSpec `json:"template,omitempty"`

	// Parallelism declares tensor/pipeline/data parallelism for the
	// role.
	// +optional
	Parallelism *ParallelismSpec `json:"parallelism,omitempty"`

	// KVCacheOffloading holds CPU and secondary-tier KV-cache
	// offloading settings. Reserved: stored but not interpreted in v0.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	KVCacheOffloading *apiextensionsv1.JSON `json:"kvCacheOffloading,omitempty"`

	// RolloutStrategy controls update behavior for the workload.
	// Reserved: stored but not interpreted in v0.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	RolloutStrategy *apiextensionsv1.JSON `json:"rolloutStrategy,omitempty"`

	// DisaggSidecar (decode role only) is injected into the decode
	// workload when configured, per the proposal's controller
	// behavior. The coordinator and the disagg sidecar are mutually
	// exclusive by llm-d-router design; v0 omits the coordinator from
	// the schema entirely.
	// +optional
	DisaggSidecar *DisaggSidecarSpec `json:"disaggSidecar,omitempty"`
}

// ParallelismSpec is the proposal's parallelism shape: tensor,
// pipeline and data parallelism.
type ParallelismSpec struct {
	// TP is tensor parallelism.
	// +optional
	// +kubebuilder:validation:Minimum=1
	TP *int32 `json:"tp,omitempty"`

	// PP is pipeline parallelism.
	// +optional
	// +kubebuilder:validation:Minimum=1
	PP *int32 `json:"pp,omitempty"`

	// DP is data parallelism.
	// +optional
	// +kubebuilder:validation:Minimum=1
	DP *int32 `json:"dp,omitempty"`
}

// RoleTemplateSpec is a role's pod template. v0 keeps only the
// containers; other pod template fields are future work.
type RoleTemplateSpec struct {
	// Containers of the role's pod. The first container is the
	// model-server engine; extra containers pass through.
	// +optional
	Containers []corev1.Container `json:"containers,omitempty"`
}

// DisaggSidecarSpec declares the disaggregation sidecar container
// injected into the decode workload.
type DisaggSidecarSpec struct {
	// Container is the sidecar container spec (e.g. the
	// llm-d-router-disagg-sidecar image). Name defaults to
	// "disagg-sidecar".
	// +required
	Container corev1.Container `json:"container"`
}

// RoutingSpec is the service's routing domain.
type RoutingSpec struct {
	// Mode selects gateway or standalone routing. Defaults to gateway.
	// v0 implements gateway only (see the README).
	// +optional
	// +kubebuilder:default=gateway
	Mode RoutingMode `json:"mode,omitempty"`

	// Gateway the HTTPRoute attaches to. The controller never creates
	// the Gateway.
	// +optional
	Gateway *GatewaySpec `json:"gateway,omitempty"`

	// HTTPRoute settings, used in mode gateway only.
	// +optional
	HTTPRoute *HTTPRouteSettings `json:"httpRoute,omitempty"`

	// Router is the llm-d-router pod domain.
	// +optional
	Router *RouterSpec `json:"router,omitempty"`

	// InferencePool settings. The pool's selector and
	// endpointPickerRef are always derived by the controller.
	// +optional
	InferencePool *InferencePoolSettings `json:"inferencePool,omitempty"`
}

// GatewaySpec lists the Gateways the service's HTTPRoute attaches to.
type GatewaySpec struct {
	// Refs are the Gateways to attach to. The first ref renders the
	// HTTPRoute parentRef; v0 uses refs[0] only.
	// +optional
	Refs []GatewayRef `json:"refs,omitempty"`
}

// GatewayRef references one Gateway.
type GatewayRef struct {
	// Provider names the Gateway implementation. Defaults to envoy.
	// +optional
	// +kubebuilder:default=envoy
	Provider GatewayProvider `json:"provider,omitempty"`

	// Name of the Gateway, in the service's namespace.
	// +required
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// HTTPRouteSettings carries the user-settable HTTPRoute fields.
type HTTPRouteSettings struct {
	// Hostnames for the HTTPRoute. When omitted the route matches all
	// hostnames the Gateway listener accepts.
	// +optional
	// +kubebuilder:validation:MaxItems=16
	Hostnames []string `json:"hostnames,omitempty"`
}

// RouterSpec is the service's llm-d-router domain.
type RouterSpec struct {
	// EPP is the Endpoint Picker main container domain.
	// +optional
	EPP *EPPSpec `json:"epp,omitempty"`
}

// EPPSpec is the service-specific EPP settings.
type EPPSpec struct {
	// Replicas is the number of EPP pods. Defaults to 1.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Replicas *int32 `json:"replicas,omitempty"`

	// EndpointPicker carries the service-specific EndpointPickerConfig.
	// +optional
	EndpointPicker *EndpointPickerSettings `json:"endpointPicker,omitempty"`
}

// EndpointPickerSettings is the service's EPP config override slot.
// The proposal puts the service's config at
// routing.router.epp.endpointPicker.inline (the profile's config lives
// at routing.router.epp.endpointPicker directly — see the README).
type EndpointPickerSettings struct {
	// Inline is the service-specific EndpointPickerConfig. It wins over
	// the profile's config, field by field.
	// +optional
	Inline *EndpointPickerConfig `json:"inline,omitempty"`
}

// InferencePoolSettings carries the user-settable slice of the
// InferencePool the controller renders (the proposal's
// routing.inferencePool.spec).
type InferencePoolSettings struct {
	// Spec holds the user-settable InferencePool fields.
	// +optional
	Spec InferencePoolOverride `json:"spec,omitempty"`
}

// InferencePoolOverride is the user-settable part of the rendered
// InferencePool. Selector and endpointPickerRef are derived.
type InferencePoolOverride struct {
	// TargetPorts are the engine container ports the pool routes to.
	// Every port is a distinct endpoint (podIP:port). Defaults to
	// 8000.
	// +optional
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=8
	TargetPorts []TargetPort `json:"targetPorts,omitempty"`
}

// TargetPort is one pool target port.
type TargetPort struct {
	// Name of the port.
	// +optional
	Name string `json:"name,omitempty"`

	// Number of the engine container port.
	// +required
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Number int32 `json:"number"`
}

// ResolvedProfileRef records the profile content a service last
// rendered from: the profile's name and the metadata.generation whose
// spec was applied. Later profile generations are not applied until
// the user accepts the drift (see AcceptProfileDriftAnnotation) or
// changes spec.profileRef.
type ResolvedProfileRef struct {
	// Name of the pinned LLMRouterServiceProfile.
	// +required
	Name string `json:"name"`

	// Generation of the pinned LLMRouterServiceProfile at render time.
	// +required
	Generation int64 `json:"generation"`
}

// LLMRouterServiceStatus defines the observed state of
// LLMRouterService. v0 reports the proposal's observedGeneration,
// Ready, WorkloadsReady, RoutingReady, ProfilesReady,
// resolvedProfileRef and endpointPickerConfigHash; the proposal's URL,
// Workloads, Routes and InferencePool status refs are future work
// (see the README).
type LLMRouterServiceStatus struct {
	// Conditions of the service.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the generation most recently reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ResolvedProfileRef pins the profile content this service's
	// children were last rendered from.
	// +optional
	ResolvedProfileRef *ResolvedProfileRef `json:"resolvedProfileRef,omitempty"`

	// EndpointPickerConfigHash is the sha256 of the merged
	// EndpointPickerConfig document rendered into the EPP's ConfigMap.
	// It makes the effective configuration observable without exposing
	// the full object in status.
	// +optional
	EndpointPickerConfigHash string `json:"endpointPickerConfigHash,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=lrs
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Reason",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].reason"
// +kubebuilder:printcolumn:name="Profile",type="string",JSONPath=".status.resolvedProfileRef.name"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// LLMRouterService is the user-facing inference service: it owns model
// identity and service-specific overrides, and turns into the complete
// serving stack (model-server workload, llm-d-router EPP,
// InferencePool, Gateway API routing) from one object.
type LLMRouterService struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   LLMRouterServiceSpec   `json:"spec,omitempty"`
	Status LLMRouterServiceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// LLMRouterServiceList contains a list of LLMRouterService.
type LLMRouterServiceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []LLMRouterService `json:"items"`
}

func init() {
	SchemeBuilder.Register(&LLMRouterService{}, &LLMRouterServiceList{})
}
