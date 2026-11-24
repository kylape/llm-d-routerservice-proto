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
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	llmdv1alpha1 "github.com/llm-d/llm-d-routerservice-proto/api/v1alpha1"
	"github.com/llm-d/llm-d-routerservice-proto/internal/kube"
	igwapi "sigs.k8s.io/gateway-api-inference-extension/api/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// Condition types reported by LLMRouterService.
const (
	CondReady          = "Ready"
	CondWorkloadsReady = "WorkloadsReady"
	CondRoutingReady   = "RoutingReady"
	CondProfilesReady  = "ProfilesReady"
)

// LLMRouterServiceReconciler reconciles an LLMRouterService into the
// complete serving stack: model-server Deployment, llm-d-router EPP
// (Deployment + Service + ConfigMap + RBAC), InferencePool and
// HTTPRoute. Children render on EXISTENCE of their inputs; readiness
// is reported via conditions only.
type LLMRouterServiceReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// ModelServerImage is the engine image used when
	// spec.workload.decode.template sets none. Defaults to the
	// llm-d-inference-sim stand-in.
	ModelServerImage string

	// EPPImage is the llm-d-router Endpoint Picker image.
	EPPImage string
}

// +kubebuilder:rbac:groups=llm-d.ai,resources=llmrouterservices,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=llm-d.ai,resources=llmrouterservices/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=llm-d.ai,resources=llmrouterserviceprofiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=configmaps;services;serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=inference.networking.k8s.io,resources=inferencepools,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,resources=httproutes,verbs=get;list;watch;create;update;patch;delete

// Reconcile implements the proposal's controller behavior: fetch the
// service, resolve and pin its profile, merge defaults under the
// spec, render the stack, and propagate readiness into conditions.
func (r *LLMRouterServiceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx).WithValues("llmrouterservice", req.NamespacedName)

	svc := &llmdv1alpha1.LLMRouterService{}
	if err := r.Get(ctx, req.NamespacedName, svc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	pristine := svc.DeepCopy()

	// 1. Resolve the profile.
	if svc.Spec.ProfileRef == "" {
		return r.reconcileNoProfile(ctx, svc, pristine)
	}

	profile := &llmdv1alpha1.LLMRouterServiceProfile{}
	if err := r.Get(ctx, types.NamespacedName{Name: svc.Spec.ProfileRef, Namespace: svc.Namespace}, profile); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, r.hold(ctx, svc, "ProfileNotFound",
			fmt.Sprintf("profile %q not found in namespace %q; children are left untouched", svc.Spec.ProfileRef, svc.Namespace))
	}
	if !profile.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.hold(ctx, svc, "ProfileTerminating",
			fmt.Sprintf("profile %q is being deleted; children are left untouched", profile.Name))
	}

	// 2. Profile pinning: the service renders from the pinned profile
	// generation. A profile whose generation has drifted is NOT
	// applied — children are left untouched and the user is told how
	// to accept — unless the accept annotation is set.
	pin := svc.Status.ResolvedProfileRef
	drifted := pin != nil && pin.Name == profile.Name && pin.Generation != profile.Generation
	accept := svc.Annotations[llmdv1alpha1.AcceptProfileDriftAnnotation] == "true"

	if drifted && !accept {
		msg := fmt.Sprintf(
			"profile %q has drifted from the pinned generation %d (current generation %d); "+
				"children are left untouched. To accept the new profile content, set the annotation %s=%q on this service (the controller re-pins to the current generation, applies it and removes the annotation), or change spec.profileRef",
			profile.Name, pin.Generation, profile.Generation, llmdv1alpha1.AcceptProfileDriftAnnotation, "true")
		return ctrl.Result{}, r.hold(ctx, svc, "ProfileDrift", msg)
	}

	// 3. Full render path: first reconcile (no pin), profileRef
	// change, accepted drift, or an unchanged pin.
	status, err := r.renderStack(ctx, svc, MergeSpec(&svc.Spec, &profile.Spec))
	if err != nil {
		return ctrl.Result{}, err
	}

	if drifted {
		log.Info("accepted profile drift", "profile", profile.Name, "fromGeneration", pin.Generation, "toGeneration", profile.Generation)
	}
	setCondition(&status.Conditions, svc.Generation, CondProfilesReady, metav1.ConditionTrue, "Ready",
		fmt.Sprintf("profile %q at generation %d is merged under the service spec", profile.Name, profile.Generation))
	status.ResolvedProfileRef = &llmdv1alpha1.ResolvedProfileRef{
		Name:       profile.Name,
		Generation: profile.Generation,
	}
	aggregateReady(svc.Generation, &status.Conditions)

	if err := r.patchStatus(ctx, svc, *status); err != nil {
		return ctrl.Result{}, err
	}

	// 4. The accept annotation is one-shot: strip it after a
	// successful render so a future drift needs a fresh opt-in.
	if err := r.stripAcceptAnnotation(ctx, svc, pristine); err != nil {
		return ctrl.Result{}, err
	}

	log.V(1).Info("reconciled llmrouterservice",
		"profile", profile.Name, "generation", profile.Generation,
		"configHash", status.EndpointPickerConfigHash)
	return ctrl.Result{}, nil
}

// reconcileNoProfile renders the stack from the service spec alone.
func (r *LLMRouterServiceReconciler) reconcileNoProfile(ctx context.Context, svc *llmdv1alpha1.LLMRouterService, pristine *llmdv1alpha1.LLMRouterService) (ctrl.Result, error) {
	status, err := r.renderStack(ctx, svc, MergeSpec(&svc.Spec, nil))
	if err != nil {
		return ctrl.Result{}, err
	}
	setCondition(&status.Conditions, svc.Generation, CondProfilesReady, metav1.ConditionTrue, "NoProfile",
		"spec.profileRef is unset; no profile defaults are applied")
	status.ResolvedProfileRef = nil
	aggregateReady(svc.Generation, &status.Conditions)

	if err := r.patchStatus(ctx, svc, *status); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.stripAcceptAnnotation(ctx, svc, pristine); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// hold leaves children untouched and reports why. Carried-forward
// status (observedGeneration, pin, hash, workload/routing conditions)
// still describes the frozen children.
func (r *LLMRouterServiceReconciler) hold(ctx context.Context, svc *llmdv1alpha1.LLMRouterService, reason, message string) error {
	status := svc.Status.DeepCopy()
	setCondition(&status.Conditions, svc.Generation, CondProfilesReady, metav1.ConditionFalse, reason, message)
	setCondition(&status.Conditions, svc.Generation, CondReady, metav1.ConditionFalse, reason, message)
	return r.patchStatus(ctx, svc, *status)
}

// renderStack renders the full serving stack from the merged spec and
// builds the status. Rejections (unsupported topology, unsupported
// routing mode) leave existing children untouched and report a False
// condition; nothing is ever deleted on rejection.
func (r *LLMRouterServiceReconciler) renderStack(ctx context.Context, svc *llmdv1alpha1.LLMRouterService, merged *MergedSpec) (*llmdv1alpha1.LLMRouterServiceStatus, error) {
	status := &llmdv1alpha1.LLMRouterServiceStatus{ObservedGeneration: svc.Generation}

	// Topology: v0 renders decode-only Deployments. Her taxonomy's
	// prefill/encode/worker roles and non-Deployment kinds are in the
	// schema but rejected here.
	if reason, msg := topologyUnsupported(&merged.Workload); reason != "" {
		setCondition(&status.Conditions, svc.Generation, CondWorkloadsReady, metav1.ConditionFalse, reason, msg)
		setCondition(&status.Conditions, svc.Generation, CondRoutingReady, metav1.ConditionFalse, reason,
			"routing children are not rendered while the workload topology is unsupported")
		return status, nil
	}

	// Routing mode: v0 implements gateway only. The workload still
	// renders; the routing stack is left untouched and reported.
	mode := merged.Routing.Mode
	if mode == "" {
		mode = llmdv1alpha1.RoutingMode("gateway")
	}
	standalone := mode == "standalone"

	// Model-server workload: source of truth for the pool selector.
	workload := r.modelServerDeployment(svc, merged)
	if err := ctrl.SetControllerReference(svc, workload, r.Scheme); err != nil {
		return nil, err
	}
	if err := kube.Upsert(ctx, r.Client, workload, func(existing client.Object) {
		existing.(*appsv1.Deployment).Spec = workload.Spec
	}); err != nil {
		return nil, fmt.Errorf("reconcile model-server deployment: %w", err)
	}
	current := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Name: workload.Name, Namespace: workload.Namespace}, current); err != nil {
		return nil, err
	}
	if deploymentAvailable(current) {
		setCondition(&status.Conditions, svc.Generation, CondWorkloadsReady, metav1.ConditionTrue, "Available",
			fmt.Sprintf("deployment %s is available", current.Name))
	} else {
		setCondition(&status.Conditions, svc.Generation, CondWorkloadsReady, metav1.ConditionFalse, "Unavailable",
			fmt.Sprintf("deployment %s is not yet available", current.Name))
	}

	if standalone {
		setCondition(&status.Conditions, svc.Generation, CondRoutingReady, metav1.ConditionFalse, "UnsupportedMode",
			"routing mode standalone is not implemented in v0; routing children are left untouched. Use mode gateway")
		return status, nil
	}

	// EPP config: merged from service and profile, defaulted to the
	// operator's profile when neither sets one.
	config := merged.Routing.EPPConfig
	if config == nil {
		config = defaultEPPConfig()
	}
	configYAML, hash, err := eppConfig(config)
	if err != nil {
		return nil, err
	}
	status.EndpointPickerConfigHash = hash

	configMap := eppConfigMap(svc, configYAML)
	if err := ctrl.SetControllerReference(svc, configMap, r.Scheme); err != nil {
		return nil, err
	}
	if err := kube.Upsert(ctx, r.Client, configMap, func(existing client.Object) {
		existing.(*corev1.ConfigMap).Data = configMap.Data
	}); err != nil {
		return nil, fmt.Errorf("reconcile epp configmap: %w", err)
	}

	// EPP Service: pools reference it whether or not the Deployment is
	// up yet.
	service := eppService(svc)
	if err := ctrl.SetControllerReference(svc, service, r.Scheme); err != nil {
		return nil, err
	}
	if err := kube.Upsert(ctx, r.Client, service, func(existing client.Object) {
		existing.(*corev1.Service).Spec.Ports = service.Spec.Ports
		existing.(*corev1.Service).Spec.Selector = service.Spec.Selector
	}); err != nil {
		return nil, fmt.Errorf("reconcile epp service: %w", err)
	}

	// EPP RBAC: the EPP watches its pool and the member pods.
	sa := eppServiceAccount(svc)
	role := eppRole(svc)
	binding := eppRoleBinding(svc)
	for _, obj := range []struct {
		desired client.Object
		mutate  func(existing client.Object)
	}{
		{sa, func(client.Object) {}}, // nothing user-settable to sync
		{role, func(existing client.Object) { existing.(*rbacv1.Role).Rules = role.Rules }},
		{binding, func(existing client.Object) {
			existing.(*rbacv1.RoleBinding).Subjects = binding.Subjects
			existing.(*rbacv1.RoleBinding).RoleRef = binding.RoleRef
		}},
	} {
		if err := ctrl.SetControllerReference(svc, obj.desired, r.Scheme); err != nil {
			return nil, err
		}
		if err := kube.Upsert(ctx, r.Client, obj.desired, obj.mutate); err != nil {
			return nil, fmt.Errorf("reconcile epp rbac: %w", err)
		}
	}

	// EPP Deployment.
	eppDeployment := r.eppDeployment(svc, merged)
	if err := ctrl.SetControllerReference(svc, eppDeployment, r.Scheme); err != nil {
		return nil, err
	}
	if err := kube.Upsert(ctx, r.Client, eppDeployment, func(existing client.Object) {
		existing.(*appsv1.Deployment).Spec = eppDeployment.Spec
	}); err != nil {
		return nil, fmt.Errorf("reconcile epp deployment: %w", err)
	}
	currentEPP := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Name: eppDeployment.Name, Namespace: eppDeployment.Namespace}, currentEPP); err != nil {
		return nil, err
	}

	// InferencePool: selector derived from the workload Deployment's
	// own selector.
	pool := inferencePool(svc, merged, current.Spec.Selector.MatchLabels)
	if err := ctrl.SetControllerReference(svc, pool, r.Scheme); err != nil {
		return nil, err
	}
	if err := kube.Upsert(ctx, r.Client, pool, func(existing client.Object) {
		existing.(*igwapi.InferencePool).Spec = pool.Spec
	}); err != nil {
		return nil, fmt.Errorf("reconcile inferencepool: %w", err)
	}

	// HTTPRoute: attach the pool to the first Gateway ref. No refs
	// means no route — the rest of the routing stack still renders.
	if len(merged.Routing.GatewayRefs) == 0 {
		setCondition(&status.Conditions, svc.Generation, CondRoutingReady, metav1.ConditionFalse, "NoGatewayRef",
			"no routing.gateway.refs set; the HTTPRoute is not rendered")
		return status, nil
	}
	route := httpRoute(svc, merged, merged.Routing.GatewayRefs[0].Name)
	if err := ctrl.SetControllerReference(svc, route, r.Scheme); err != nil {
		return nil, err
	}
	if err := kube.Upsert(ctx, r.Client, route, func(existing client.Object) {
		existing.(*gwapiv1.HTTPRoute).Spec = route.Spec
	}); err != nil {
		return nil, fmt.Errorf("reconcile httproute: %w", err)
	}

	if !deploymentAvailable(currentEPP) {
		setCondition(&status.Conditions, svc.Generation, CondRoutingReady, metav1.ConditionFalse, "EPPUnavailable",
			fmt.Sprintf("epp deployment %s is not yet available", currentEPP.Name))
		return status, nil
	}
	setCondition(&status.Conditions, svc.Generation, CondRoutingReady, metav1.ConditionTrue, "Wired",
		fmt.Sprintf("epp %s serves pool %s; httproute %s attaches the pool to gateway %s",
			service.Name, pool.Name, route.Name, merged.Routing.GatewayRefs[0].Name))
	return status, nil
}

// SetupWithManager wires the service to its owned children and to the
// profiles it references.
func (r *LLMRouterServiceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&llmdv1alpha1.LLMRouterService{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Owns(&igwapi.InferencePool{}).
		Owns(&gwapiv1.HTTPRoute{}).
		Watches(&llmdv1alpha1.LLMRouterServiceProfile{}, handler.EnqueueRequestsFromMapFunc(r.profilesToServices)).
		Complete(r)
}

// profilesToServices requeues every service referencing a changed
// profile: profile changes must surface as drift conditions.
func (r *LLMRouterServiceReconciler) profilesToServices(ctx context.Context, obj client.Object) []reconcile.Request {
	list := &llmdv1alpha1.LLMRouterServiceList{}
	if err := r.List(ctx, list, client.InNamespace(obj.GetNamespace())); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		if list.Items[i].Spec.ProfileRef == obj.GetName() {
			reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{
				Namespace: list.Items[i].Namespace, Name: list.Items[i].Name,
			}})
		}
	}
	return reqs
}

// stripAcceptAnnotation removes the one-shot accept annotation after a
// successful render, so a future drift needs a fresh opt-in. The
// pristine copy predates any in-memory status mutation, so the patch
// touches only the annotation.
func (r *LLMRouterServiceReconciler) stripAcceptAnnotation(ctx context.Context, svc *llmdv1alpha1.LLMRouterService, pristine *llmdv1alpha1.LLMRouterService) error {
	if _, found := pristine.Annotations[llmdv1alpha1.AcceptProfileDriftAnnotation]; !found {
		return nil
	}
	patched := pristine.DeepCopy()
	delete(patched.Annotations, llmdv1alpha1.AcceptProfileDriftAnnotation)
	if err := r.Patch(ctx, patched, client.MergeFrom(pristine)); err != nil {
		return fmt.Errorf("strip %s annotation: %w", llmdv1alpha1.AcceptProfileDriftAnnotation, err)
	}
	return nil
}

func (r *LLMRouterServiceReconciler) patchStatus(ctx context.Context, svc *llmdv1alpha1.LLMRouterService, status llmdv1alpha1.LLMRouterServiceStatus) error {
	if statusEqual(svc.Status, status) {
		return nil
	}
	svc.Status = status
	return r.Status().Update(ctx, svc)
}

func statusEqual(a, b llmdv1alpha1.LLMRouterServiceStatus) bool {
	if a.ObservedGeneration != b.ObservedGeneration ||
		a.EndpointPickerConfigHash != b.EndpointPickerConfigHash {
		return false
	}
	if (a.ResolvedProfileRef == nil) != (b.ResolvedProfileRef == nil) {
		return false
	}
	if a.ResolvedProfileRef != nil && *a.ResolvedProfileRef != *b.ResolvedProfileRef {
		return false
	}
	return conditionsEqual(a.Conditions, b.Conditions)
}

// topologyUnsupported reports whether the merged workload selects a
// topology v0 cannot render (anything but a decode-only Deployment).
func topologyUnsupported(workload *llmdv1alpha1.WorkloadSpec) (reason, message string) {
	if workload.Kind != "" && workload.Kind != "Deployment" {
		return "UnsupportedTopology",
			fmt.Sprintf("workload kind %q is not supported in v0; only Deployment is implemented", workload.Kind)
	}
	for _, role := range []struct {
		name string
		spec *llmdv1alpha1.WorkloadRoleSpec
	}{
		{"prefill", workload.Prefill},
		{"encode", workload.Encode},
		{"worker", workload.Worker},
	} {
		if role.spec != nil {
			return "UnsupportedTopology",
				fmt.Sprintf("workload role %q selects a disaggregated or multi-node topology; v0 implements decode-only Deployments only", role.name)
		}
	}
	return "", ""
}

// aggregateReady sets Ready from the child conditions, surfacing the
// first failing sub-condition's reason and message.
func aggregateReady(generation int64, conditions *[]metav1.Condition) {
	for _, t := range []string{CondProfilesReady, CondWorkloadsReady, CondRoutingReady} {
		cond := meta.FindStatusCondition(*conditions, t)
		if cond == nil {
			continue
		}
		if cond.Status != metav1.ConditionTrue {
			setCondition(conditions, generation, CondReady, metav1.ConditionFalse, cond.Reason, cond.Message)
			return
		}
	}
	setCondition(conditions, generation, CondReady, metav1.ConditionTrue, "Ready",
		"workloads, routing and profiles are ready")
}

func setCondition(conditions *[]metav1.Condition, generation int64, condType string, condStatus metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(conditions, metav1.Condition{
		Type:               condType,
		Status:             condStatus,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: generation,
	})
}

func deploymentAvailable(d *appsv1.Deployment) bool {
	for _, c := range d.Status.Conditions {
		if c.Type == appsv1.DeploymentAvailable && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func conditionsEqual(a, b []metav1.Condition) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Type != b[i].Type || a[i].Status != b[i].Status || a[i].Reason != b[i].Reason || a[i].Message != b[i].Message {
			return false
		}
	}
	return true
}
