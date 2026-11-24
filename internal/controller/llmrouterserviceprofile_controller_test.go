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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	llmdv1alpha1 "github.com/llm-d/llm-d-routerservice-proto/api/v1alpha1"
)

// TestProfileStatusReportsReferences verifies the proposal's profile
// status: observedGeneration, Ready and ReferencedBy.
func TestProfileStatusReportsReferences(t *testing.T) {
	ctx := context.Background()
	c, _, r := newTestHarness(t)

	profile := profileWithConfig("gold", 3, "queue-scorer")
	require.NoError(t, c.Create(ctx, profile))
	require.NoError(t, c.Create(ctx, demoService("qwen", "gold")))
	require.NoError(t, c.Create(ctx, demoService("llama", "gold")))
	require.NoError(t, c.Create(ctx, demoService("mistral", "")))

	_, err := r.Reconcile(ctx, requestFor("gold"))
	require.NoError(t, err)

	current := &llmdv1alpha1.LLMRouterServiceProfile{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "gold", Namespace: testNamespace}, current))
	assert.Equal(t, int64(3), current.Status.ObservedGeneration)
	assert.ElementsMatch(t, []string{"qwen", "llama"}, current.Status.ReferencedBy)
	assertCondition(t, current.Status.Conditions, "Ready", true, "Ready")

	// Removing a reference updates ReferencedBy.
	dropped := &llmdv1alpha1.LLMRouterService{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "llama", Namespace: testNamespace}, dropped))
	dropped.Spec.ProfileRef = ""
	require.NoError(t, c.Update(ctx, dropped))

	_, err = r.Reconcile(ctx, requestFor("gold"))
	require.NoError(t, err)
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "gold", Namespace: testNamespace}, current))
	assert.ElementsMatch(t, []string{"qwen"}, current.Status.ReferencedBy)
}

// TestProfileTerminatingReportsNotReady verifies a deleting profile
// reports Ready=False.
func TestProfileTerminatingReportsNotReady(t *testing.T) {
	ctx := context.Background()
	c, _, r := newTestHarness(t)

	profile := profileWithConfig("gold", 1, "queue-scorer")
	// Mark the profile terminating: the fake client only sets
	// deletionTimestamp through Delete, and only keeps the object when
	// a finalizer holds it.
	profile.Finalizers = []string{"test.llm-d.ai/hold"}
	require.NoError(t, c.Create(ctx, profile))
	require.NoError(t, c.Delete(ctx, profile))

	_, err := r.Reconcile(ctx, requestFor("gold"))
	require.NoError(t, err)

	current := &llmdv1alpha1.LLMRouterServiceProfile{}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "gold", Namespace: testNamespace}, current))
	assertCondition(t, current.Status.Conditions, "Ready", false, "Terminating")
}

// TestServicesToProfilesMap verifies the service watch mapping.
func TestServicesToProfilesMap(t *testing.T) {
	_, _, r := newTestHarness(t)

	reqs := r.servicesToProfiles(context.Background(), demoService("qwen", "gold"))
	require.Len(t, reqs, 1)
	assert.Equal(t, "gold", reqs[0].Name)
	assert.Equal(t, testNamespace, reqs[0].Namespace)

	assert.Empty(t, r.servicesToProfiles(context.Background(), demoService("x", "")))
}
