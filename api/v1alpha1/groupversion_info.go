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

// Package v1alpha1 contains the llm-d.ai/v1alpha1 API group: the
// LLMRouterService and LLMRouterServiceProfile CRDs from the
// LLMRouterService proposal. One flat group holds
// both resources; LLMBatchGateway is deliberately not implemented
// here (see the README).
// +kubebuilder:object:generate=true
// +groupName=llm-d.ai
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is the group/version of the llm-d.ai API family.
	GroupVersion = schema.GroupVersion{Group: "llm-d.ai", Version: "v1alpha1"}

	// SchemeBuilder registers this group's types into a scheme.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds this group's types into a scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)
