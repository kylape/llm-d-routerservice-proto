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
	llmdv1alpha1 "github.com/llm-d/llm-d-routerservice-proto/api/v1alpha1"
)

// MergedSpec is the effective LLMRouterService spec after profile
// defaults are deep-merged underneath the service spec. It normalizes
// the service/profile field asymmetry (the service's EPP config lives
// at routing.router.epp.endpointPicker.inline, the profile's at
// routing.router.epp.endpointPicker) into single slots.
//
// Merge semantics, per the proposal's field-ownership table: the
// service wins everywhere it sets a field; the profile fills unset
// fields. Structs merge field-wise; lists replace wholesale (a
// non-empty service list wins, an empty one falls back to the
// profile's).
type MergedSpec struct {
	// Workload is the merged model-server domain.
	Workload llmdv1alpha1.WorkloadSpec

	// Routing is the merged routing domain.
	Routing MergedRouting
}

// MergedRouting is the merged routing domain.
type MergedRouting struct {
	// Mode is the routing mode; "" defaults to gateway at render time.
	Mode llmdv1alpha1.RoutingMode

	// GatewayRefs are the Gateway refs for the HTTPRoute parentRef.
	GatewayRefs []llmdv1alpha1.GatewayRef

	// Hostnames for the HTTPRoute.
	Hostnames []string

	// EPPReplicas is the EPP Deployment replica count; nil defaults to
	// 1 at render time.
	EPPReplicas *int32

	// EPPConfig is the merged EndpointPickerConfig; nil defaults to
	// the controller's default config at render time.
	EPPConfig *llmdv1alpha1.EndpointPickerConfig

	// PoolTargetPorts are the InferencePool target ports; empty
	// defaults to 8000 at render time.
	PoolTargetPorts []llmdv1alpha1.TargetPort
}

// MergeSpec deep-merges the profile's defaults under the service spec
// and returns the effective spec. Either input may carry unset
// sections; the returned MergedSpec never aliases the caller's
// objects.
func MergeSpec(service *llmdv1alpha1.LLMRouterServiceSpec, profile *llmdv1alpha1.LLMRouterServiceProfileSpec) *MergedSpec {
	s := service.DeepCopy()
	m := &MergedSpec{Workload: s.Workload}

	// Service routing slots.
	if s.Routing.Mode != "" {
		m.Routing.Mode = s.Routing.Mode
	}
	if s.Routing.Gateway != nil {
		m.Routing.GatewayRefs = s.Routing.Gateway.Refs
	}
	if s.Routing.HTTPRoute != nil {
		m.Routing.Hostnames = s.Routing.HTTPRoute.Hostnames
	}
	if s.Routing.Router != nil && s.Routing.Router.EPP != nil {
		m.Routing.EPPReplicas = s.Routing.Router.EPP.Replicas
		if s.Routing.Router.EPP.EndpointPicker != nil {
			m.Routing.EPPConfig = s.Routing.Router.EPP.EndpointPicker.Inline
		}
	}
	if s.Routing.InferencePool != nil {
		m.Routing.PoolTargetPorts = s.Routing.InferencePool.Spec.TargetPorts
	}

	if profile == nil {
		return m
	}
	p := profile.DeepCopy()

	if p.Workload != nil {
		mergeWorkload(&m.Workload, p.Workload)
	}

	if p.Routing != nil {
		pr := p.Routing
		if m.Routing.Mode == "" {
			m.Routing.Mode = pr.Mode
		}
		if len(m.Routing.GatewayRefs) == 0 && pr.Gateway != nil {
			m.Routing.GatewayRefs = pr.Gateway.Refs
		}
		if len(m.Routing.Hostnames) == 0 && pr.HTTPRoute != nil {
			m.Routing.Hostnames = pr.HTTPRoute.Hostnames
		}
		if m.Routing.EPPReplicas == nil && pr.Router != nil && pr.Router.EPP != nil {
			m.Routing.EPPReplicas = pr.Router.EPP.Replicas
		}
		var profileConfig *llmdv1alpha1.EndpointPickerConfig
		if pr.Router != nil && pr.Router.EPP != nil {
			profileConfig = pr.Router.EPP.EndpointPicker
		}
		m.Routing.EPPConfig = mergeEPPConfig(m.Routing.EPPConfig, profileConfig)
		if len(m.Routing.PoolTargetPorts) == 0 && pr.InferencePool != nil {
			m.Routing.PoolTargetPorts = pr.InferencePool.Spec.TargetPorts
		}
	}

	return m
}

// mergeWorkload fills unset workload fields from the profile's
// workload defaults, in place.
func mergeWorkload(dst *llmdv1alpha1.WorkloadSpec, defaults *llmdv1alpha1.WorkloadSpec) {
	if dst.Kind == "" {
		dst.Kind = defaults.Kind
	}
	dst.Prefill = mergeRole(dst.Prefill, defaults.Prefill)
	dst.Decode = mergeRole(dst.Decode, defaults.Decode)
	dst.Encode = mergeRole(dst.Encode, defaults.Encode)
	dst.Worker = mergeRole(dst.Worker, defaults.Worker)
}

// mergeRole merges one role: a nil service role adopts the profile's
// role; otherwise the service's set fields win and the profile fills
// the unset ones.
func mergeRole(dst, defaults *llmdv1alpha1.WorkloadRoleSpec) *llmdv1alpha1.WorkloadRoleSpec {
	if defaults == nil {
		return dst
	}
	if dst == nil {
		return defaults
	}
	if dst.Replicas == nil {
		dst.Replicas = defaults.Replicas
	}
	if dst.Template == nil {
		dst.Template = defaults.Template
	} else if defaults.Template != nil && len(dst.Template.Containers) == 0 {
		dst.Template.Containers = defaults.Template.Containers
	}
	if dst.Parallelism == nil {
		dst.Parallelism = defaults.Parallelism
	} else if defaults.Parallelism != nil {
		if dst.Parallelism.TP == nil {
			dst.Parallelism.TP = defaults.Parallelism.TP
		}
		if dst.Parallelism.PP == nil {
			dst.Parallelism.PP = defaults.Parallelism.PP
		}
		if dst.Parallelism.DP == nil {
			dst.Parallelism.DP = defaults.Parallelism.DP
		}
	}
	if dst.KVCacheOffloading == nil {
		dst.KVCacheOffloading = defaults.KVCacheOffloading
	}
	if dst.RolloutStrategy == nil {
		dst.RolloutStrategy = defaults.RolloutStrategy
	}
	if dst.DisaggSidecar == nil {
		dst.DisaggSidecar = defaults.DisaggSidecar
	}
	return dst
}

// mergeEPPConfig merges the service's inline EndpointPickerConfig over
// the profile's config, field by field at the document's top level:
// for each of the config's fields, the service's value wins when set
// and the profile's fills otherwise. Nested documents (dataLayer,
// flowControl, ...) replace wholesale.
func mergeEPPConfig(service, profile *llmdv1alpha1.EndpointPickerConfig) *llmdv1alpha1.EndpointPickerConfig {
	switch {
	case service == nil:
		return profile
	case profile == nil:
		return service
	}
	out := service.DeepCopy()
	if len(out.FeatureGates) == 0 {
		out.FeatureGates = profile.FeatureGates
	}
	if len(out.Plugins) == 0 {
		out.Plugins = profile.Plugins
	}
	if len(out.SchedulingProfiles) == 0 {
		out.SchedulingProfiles = profile.SchedulingProfiles
	}
	if out.DataLayer == nil {
		out.DataLayer = profile.DataLayer
	}
	if out.FlowControl == nil {
		out.FlowControl = profile.FlowControl
	}
	if out.RequestHandler == nil {
		out.RequestHandler = profile.RequestHandler
	}
	if out.SaturationDetector == nil {
		out.SaturationDetector = profile.SaturationDetector
	}
	if out.Parser == nil {
		out.Parser = profile.Parser
	}
	return out
}
