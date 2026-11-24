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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// The EndpointPickerConfig type tree below is vendored from
// llm-d-router's apix/config/v1alpha1 (github.com/llm-d/llm-d-router),
// where it is a "pseudo CRD": a serialization schema for a config
// document, never served as an API. Vendoring it here promotes it to
// typed fields on LLMRouterService (routing.router.epp.endpointPicker.inline)
// and LLMRouterServiceProfile (routing.router.epp.endpointPicker).
//
// Adaptations from upstream:
//   - json.RawMessage parameters became apiextensionsv1.JSON so the CRD
//     schema preserves arbitrary plugin parameter shapes
//     (+kubebuilder:pruning:PreserveUnknownFields + Schemaless)
//   - TypeMeta is dropped; apiVersion/kind are added at ConfigMap
//     render time (llm-d.ai/v1, EndpointPickerConfig)
//   - String() helpers are dropped
//
// When upstream promotes this type to a real CRD, switch the fields to
// reference it and delete this copy.

// EndpointPickerConfig is the configuration for the llm-d-router
// Endpoint Picker (EPP).
type EndpointPickerConfig struct {
	// FeatureGates is a set of flags that enable various experimental
	// features with the EPP. If omitted none of these experimental
	// features will be enabled.
	// +optional
	FeatureGates FeatureGates `json:"featureGates,omitempty"`

	// Plugins is the list of plugins that will be instantiated.
	// +required
	// +kubebuilder:validation:Required
	Plugins []PluginSpec `json:"plugins"`

	// SchedulingProfiles is the list of named SchedulingProfiles
	// that will be created.
	// +required
	// +kubebuilder:validation:Required
	SchedulingProfiles []SchedulingProfile `json:"schedulingProfiles"`

	// DataLayer configures the DataLayer. It is required if the new
	// DataLayer is enabled.
	// +optional
	DataLayer *DataLayerConfig `json:"dataLayer,omitempty"`

	// FlowControl configures the Flow Control layer.
	// This configuration is only respected if the "flowControl"
	// FeatureGate is enabled.
	// +optional
	FlowControl *FlowControlConfig `json:"flowControl,omitempty"`

	// RequestHandler specifies the handling logic used by the EPP to
	// process incoming requests.
	// +optional
	RequestHandler *RequestHandlerConfig `json:"requestHandler,omitempty"`

	// SaturationDetector specifies which saturation detector plugin to use.
	//
	// Deprecated: use flowControl.saturationDetector instead. If both are
	// set, the new field is used.
	// +optional
	SaturationDetector *SaturationDetectorConfig `json:"saturationDetector,omitempty"`

	// Parser specifies the parsing logic used by the EPP to process
	// protocol messages.
	//
	// Deprecated: use requestHandler.parsers instead. If both are set,
	// the new field is used.
	// +optional
	Parser *ParserConfig `json:"parser,omitempty"`
}

// PluginSpec contains the information that describes a plugin that
// will be instantiated.
type PluginSpec struct {
	// Name provides a name for plugin entries to reference. If
	// omitted, the value of the Plugin's Type field will be used.
	// +optional
	Name string `json:"name,omitempty"`

	// Type specifies the plugin type to be instantiated.
	// +required
	// +kubebuilder:validation:Required
	Type string `json:"type"`

	// Parameters are the set of parameters to be passed to the plugin's
	// factory function. The factory function is responsible for parsing
	// the parameters.
	// +optional
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	Parameters *apiextensionsv1.JSON `json:"parameters,omitempty"`
}

// SchedulingProfile contains the information to create a
// SchedulingProfile entry to be used by the scheduler.
type SchedulingProfile struct {
	// Name specifies the name of this SchedulingProfile.
	// +required
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Plugins is the list of plugins for this SchedulingProfile. They
	// are assigned to the appropriate "slots" based on their type.
	// +required
	// +kubebuilder:validation:Required
	Plugins []SchedulingPlugin `json:"plugins"`
}

// SchedulingPlugin describes a plugin that will be associated with
// this SchedulingProfile entry.
type SchedulingPlugin struct {
	// PluginRef specifies a particular Plugin instance to be associated
	// with this SchedulingProfile. The reference is to the name of an
	// entry of the Plugins defined in the configuration's Plugins
	// section.
	// +required
	// +kubebuilder:validation:Required
	PluginRef string `json:"pluginRef"`

	// Weight is the weight to be used if this plugin is a Scorer.
	// +optional
	Weight *float64 `json:"weight,omitempty"`
}

// FeatureGates is a set of flags that enable various experimental
// features with the EPP.
type FeatureGates []string

// SaturationDetectorConfig contains the configuration for a
// saturation detector.
type SaturationDetectorConfig struct {
	// PluginRef specifies the name of the plugin instance to use for
	// saturation detection. The reference is to the name of an entry of
	// the Plugins defined in the configuration's Plugins section. If
	// unspecified, "utilization-detector" is used by default.
	// +optional
	PluginRef string `json:"pluginRef,omitempty"`
}

// DataLayerConfig contains the configuration of the DataLayer feature.
type DataLayerConfig struct {
	// InjectDefaults controls automatic injection of the default metrics
	// source and extractor. Defaults to true when omitted. Set to false
	// to disable all default source injection.
	// +optional
	InjectDefaults *bool `json:"injectDefaults,omitempty"`

	// Sources is the list of sources to define to the DataLayer.
	// +optional
	Sources []DataLayerSource `json:"sources,omitempty"`

	// Discovery specifies which EndpointDiscovery plugin to use for
	// populating the endpoint datastore. If omitted, the EPP uses the
	// default Kubernetes-based discovery.
	// +optional
	Discovery *DiscoveryConfig `json:"discovery,omitempty"`
}

// DiscoveryConfig references the EndpointDiscovery plugin to use.
type DiscoveryConfig struct {
	// PluginRef is the name of the plugin instance (from the Plugins
	// list) that implements EndpointDiscovery.
	// +required
	// +kubebuilder:validation:Required
	PluginRef string `json:"pluginRef"`
}

// DataLayerSource contains the configuration of a DataSource of the
// DataLayer feature.
type DataLayerSource struct {
	// PluginRef specifies a particular Plugin instance to be associated
	// with this Source.
	// +required
	// +kubebuilder:validation:Required
	PluginRef string `json:"pluginRef"`

	// Extractors specifies the list of Plugin instances to be
	// associated with this Source.
	// +required
	// +kubebuilder:validation:Required
	Extractors []DataLayerExtractor `json:"extractors"`
}

// DataLayerExtractor contains the configuration of an Extractor of the
// DataLayer feature.
type DataLayerExtractor struct {
	// PluginRef specifies a particular Plugin instance to be associated
	// with this Extractor.
	// +required
	// +kubebuilder:validation:Required
	PluginRef string `json:"pluginRef"`
}

// RequestHandlerConfig contains the configuration for incoming request
// handling.
type RequestHandlerConfig struct {
	// Parsers specifies the parsing plugins used by the EPP to process
	// protocol messages. If unspecified, default parsing behavior will
	// be applied.
	// +optional
	Parsers []ParserConfig `json:"parsers,omitempty"`
}

// ParserConfig contains the configuration for a parser.
type ParserConfig struct {
	// PluginRef specifies a particular Plugin instance to be associated
	// with this Parser. Default: openai-parser.
	// +required
	// +kubebuilder:validation:Required
	PluginRef string `json:"pluginRef"`
}

// FlowControlConfig configures the Flow Control layer.
type FlowControlConfig struct {
	// MaxBytes defines the global maximum aggregate byte size of all
	// active requests across all priority levels. Accepts standard
	// Kubernetes resource quantities (e.g., "1Gi", "500M"). If omitted,
	// no global byte limit is enforced.
	// +optional
	MaxBytes *resource.Quantity `json:"maxBytes,omitempty"`

	// MaxRequests defines the global maximum number of concurrent
	// requests across all priority levels. Accepts standard
	// Kubernetes resource quantities (e.g., "100", "1k"). If omitted, no
	// global request limit is enforced.
	// +optional
	MaxRequests *resource.Quantity `json:"maxRequests,omitempty"`

	// DefaultRequestTTL serves as a fallback timeout for requests that
	// do not specify their own deadline. If 0 or omitted, it defaults to
	// the client context deadline.
	// +optional
	DefaultRequestTTL *metav1.Duration `json:"defaultRequestTTL,omitempty"`

	// DefaultPriorityBand defines a template for handling traffic with
	// priority levels not explicitly configured in PriorityBands. If not
	// specified, a system-default template is used.
	// +optional
	DefaultPriorityBand *PriorityBandConfig `json:"defaultPriorityBand,omitempty"`

	// DefaultNegativePriorityBand defines a separate template for
	// priority levels strictly below zero. If not specified, negative
	// priorities fall back to DefaultPriorityBand.
	// +optional
	DefaultNegativePriorityBand *PriorityBandConfig `json:"defaultNegativePriorityBand,omitempty"`

	// PriorityBands explicitly defines policies for specific priority
	// levels. Traffic matching these priorities will be handled
	// according to these rules.
	// +optional
	PriorityBands []PriorityBandConfig `json:"priorityBands,omitempty"`

	// UsageLimitPolicyPluginRef specifies the UsageLimitPolicy plugin to
	// use for adaptive capacity management. Must reference a named
	// plugin instance defined in the top-level Plugins section. If
	// omitted, a default static policy is used.
	// +optional
	UsageLimitPolicyPluginRef string `json:"usageLimitPolicyPluginRef,omitempty"`

	// SaturationDetector specifies which saturation detector plugin to
	// use for both Admission and Flow Control. If omitted,
	// "utilization-detector" is used by default.
	// +optional
	SaturationDetector *SaturationDetectorConfig `json:"saturationDetector,omitempty"`
}

// PriorityBandConfig configures a single priority band.
type PriorityBandConfig struct {
	// Priority is the integer priority level for this band. Higher
	// values indicate higher priority.
	// +required
	Priority int `json:"priority"`

	// MaxBytes is the maximum number of bytes allowed for this priority
	// band. If omitted, the system default is used.
	// +optional
	MaxBytes *resource.Quantity `json:"maxBytes,omitempty"`

	// MaxRequests is the maximum number of concurrent requests allowed
	// for this priority band. If omitted, no request limit is enforced.
	// +optional
	MaxRequests *resource.Quantity `json:"maxRequests,omitempty"`

	// FairnessPolicyRef specifies the name of the policy that governs
	// flow selection. If omitted, the system default is used.
	// +optional
	FairnessPolicyRef string `json:"fairnessPolicyRef,omitempty"`

	// OrderingPolicyRef specifies the name of the policy that governs
	// request selection within a flow. If omitted, the system default
	// is used.
	// +optional
	OrderingPolicyRef string `json:"orderingPolicyRef,omitempty"`
}

// EPPConfigDocument is the wire format written to the EPP's ConfigMap:
// the vendored config plus the TypeMeta the EPP loader expects.
type EPPConfigDocument struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`

	EndpointPickerConfig `json:",inline"`
}

// EPPDefaultResources is the default resource envelope for the EPP
// container, mirroring the chart's base values.
func EPPDefaultResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("500m"),
			corev1.ResourceMemory: resource.MustParse("512Mi"),
		},
	}
}
