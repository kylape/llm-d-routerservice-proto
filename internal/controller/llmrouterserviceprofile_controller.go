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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	llmdv1alpha1 "github.com/llm-d/llm-d-routerservice-proto/api/v1alpha1"
)

// LLMRouterServiceProfileReconciler reconciles an
// LLMRouterServiceProfile. A profile is pure data: it renders no
// children. The reconciler reports the proposal's profile status —
// observedGeneration, Ready and ReferencedBy — so referencing services
// (and their users) can observe who consumes a profile.
type LLMRouterServiceProfileReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=llm-d.ai,resources=llmrouterserviceprofiles,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=llm-d.ai,resources=llmrouterserviceprofiles/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=llm-d.ai,resources=llmrouterservices,verbs=get;list;watch

// Reconcile reports the profile's references. Profiles are validated
// at CRD admission; a profile that exists and is not being deleted is
// Ready.
func (r *LLMRouterServiceProfileReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx).WithValues("llmrouterserviceprofile", req.NamespacedName)

	profile := &llmdv1alpha1.LLMRouterServiceProfile{}
	if err := r.Get(ctx, req.NamespacedName, profile); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	status := llmdv1alpha1.LLMRouterServiceProfileStatus{ObservedGeneration: profile.Generation}

	services := &llmdv1alpha1.LLMRouterServiceList{}
	if err := r.List(ctx, services, client.InNamespace(profile.Namespace)); err != nil {
		return ctrl.Result{}, err
	}
	for i := range services.Items {
		if services.Items[i].Spec.ProfileRef == profile.Name {
			status.ReferencedBy = append(status.ReferencedBy, services.Items[i].Name)
		}
	}

	if !profile.DeletionTimestamp.IsZero() {
		setCondition(&status.Conditions, profile.Generation, "Ready", metav1.ConditionFalse, "Terminating",
			"the profile is being deleted; referencing services hold their children")
	} else {
		setCondition(&status.Conditions, profile.Generation, "Ready", metav1.ConditionTrue, "Ready",
			"profile is valid and available for reference")
	}

	if profileStatusEqual(profile.Status, status) {
		return ctrl.Result{}, nil
	}
	profile.Status = status
	if err := r.Status().Update(ctx, profile); err != nil {
		return ctrl.Result{}, err
	}
	log.V(1).Info("reconciled llmrouterserviceprofile", "referencedBy", status.ReferencedBy)
	return ctrl.Result{}, nil
}

// SetupWithManager wires the profile to the services that reference
// it, so reference changes update ReferencedBy promptly.
func (r *LLMRouterServiceProfileReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&llmdv1alpha1.LLMRouterServiceProfile{}).
		Watches(&llmdv1alpha1.LLMRouterService{}, handler.EnqueueRequestsFromMapFunc(r.servicesToProfiles)).
		Complete(r)
}

// servicesToProfiles requeues the profiles a changed service
// references.
func (r *LLMRouterServiceProfileReconciler) servicesToProfiles(ctx context.Context, obj client.Object) []reconcile.Request {
	svc, ok := obj.(*llmdv1alpha1.LLMRouterService)
	if !ok || svc.Spec.ProfileRef == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{
		Namespace: svc.Namespace, Name: svc.Spec.ProfileRef,
	}}}
}

func profileStatusEqual(a, b llmdv1alpha1.LLMRouterServiceProfileStatus) bool {
	if a.ObservedGeneration != b.ObservedGeneration || len(a.ReferencedBy) != len(b.ReferencedBy) {
		return false
	}
	for i := range a.ReferencedBy {
		if a.ReferencedBy[i] != b.ReferencedBy[i] {
			return false
		}
	}
	return conditionsEqual(a.Conditions, b.Conditions)
}
