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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// LLMRouterServiceProfileSpec defines the desired state of
// LLMRouterServiceProfile: a reusable preset of infrastructure
// defaults. It deliberately does not own model identity or
// service-specific values; a referencing LLMRouterService wins
// everywhere both set a field.
type LLMRouterServiceProfileSpec struct {
	// Workload defaults (kind and role templates) for referencing
	// services.
	// +optional
	Workload *WorkloadSpec `json:"workload,omitempty"`

	// Routing defaults for referencing services.
	// +optional
	Routing *RoutingProfileSpec `json:"routing,omitempty"`
}

// RoutingProfileSpec is the profile's routing domain. It mirrors the
// service's RoutingSpec except for the EPP config slot: the profile
// keeps its EndpointPickerConfig at routing.router.epp.endpointPicker
// directly (the service wraps its override in ...endpointPicker.inline),
// matching the proposal's written paths verbatim.
type RoutingProfileSpec struct {
	// Mode default for referencing services.
	// +optional
	Mode RoutingMode `json:"mode,omitempty"`

	// Gateway default for referencing services.
	// +optional
	Gateway *GatewaySpec `json:"gateway,omitempty"`

	// HTTPRoute default for referencing services.
	// +optional
	HTTPRoute *HTTPRouteSettings `json:"httpRoute,omitempty"`

	// Router is the llm-d-router defaults domain.
	// +optional
	Router *RouterProfileSpec `json:"router,omitempty"`

	// InferencePool default for referencing services.
	// +optional
	InferencePool *InferencePoolSettings `json:"inferencePool,omitempty"`
}

// RouterProfileSpec is the profile's llm-d-router domain.
type RouterProfileSpec struct {
	// EPP defaults for referencing services.
	// +optional
	EPP *EPPProfileSpec `json:"epp,omitempty"`
}

// EPPProfileSpec is the profile's EPP defaults.
type EPPProfileSpec struct {
	// Replicas default for the EPP Deployment. The service's own value
	// wins.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Replicas *int32 `json:"replicas,omitempty"`

	// EndpointPicker is the reusable default EndpointPickerConfig. The
	// service's routing.router.epp.endpointPicker.inline wins, field by
	// field.
	// +optional
	EndpointPicker *EndpointPickerConfig `json:"endpointPicker,omitempty"`
}

// LLMRouterServiceProfileStatus defines the observed state of
// LLMRouterServiceProfile.
type LLMRouterServiceProfileStatus struct {
	// Conditions of the profile.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// ObservedGeneration is the generation most recently reconciled.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// ReferencedBy lists the names of LLMRouterServices in this
	// namespace that reference the profile via spec.profileRef.
	// +optional
	// +listType=atomic
	ReferencedBy []string `json:"referencedBy,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=lrsp
// +kubebuilder:printcolumn:name="Ready",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="Referenced By",type="string",JSONPath=".status.referencedBy[*]"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// LLMRouterServiceProfile is a reusable preset of infrastructure
// defaults for LLMRouterService. Profiles isolate release-specific
// knowledge from services; a service picks up changed profile content
// only through the profile-pinning mechanism (see
// LLMRouterServiceStatus.ResolvedProfileRef).
type LLMRouterServiceProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   LLMRouterServiceProfileSpec   `json:"spec,omitempty"`
	Status LLMRouterServiceProfileStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// LLMRouterServiceProfileList contains a list of
// LLMRouterServiceProfile.
type LLMRouterServiceProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []LLMRouterServiceProfile `json:"items"`
}

func init() {
	SchemeBuilder.Register(&LLMRouterServiceProfile{}, &LLMRouterServiceProfileList{})
}
