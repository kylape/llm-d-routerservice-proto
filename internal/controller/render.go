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
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"

	llmdv1alpha1 "github.com/llm-d/llm-d-routerservice-proto/api/v1alpha1"
	igwapi "sigs.k8s.io/gateway-api-inference-extension/api/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	// EPPGRPCPort is the ext-proc port the EPP serves and
	// InferencePools reference in their endpointPickerRef.
	EPPGRPCPort int32 = 9002
	// EPPHealthPort is the EPP's gRPC health port.
	EPPHealthPort int32 = 9003
	// EPPMetricsPort is the EPP's Prometheus port.
	EPPMetricsPort int32 = 9090

	// DefaultTargetPort is the engine container port assumed when
	// routing.inferencePool.spec.targetPorts is unset.
	DefaultTargetPort int32 = 8000

	// PoolGroup is the GAIE API group the EPP watches pools in.
	PoolGroup = "inference.networking.k8s.io"

	// ModelServerComponent labels model-server children.
	ModelServerComponent = "model-server"
	// EPPComponent labels EPP children.
	EPPComponent = "epp"
)

// childNames derives the deterministic child object names for one
// service.
type childNames struct {
	ModelServer string
	EPP         string
	EPPConfig   string
	Pool        string
	Route       string
}

func newChildNames(service string) childNames {
	return childNames{
		ModelServer: service + "-model-server",
		EPP:         service + "-epp",
		EPPConfig:   service + "-epp-config",
		Pool:        service,
		Route:       service,
	}
}

// ownedLabels are the deterministic labels on every rendered child;
// the model-server and EPP Deployments also use them as pod selectors.
func ownedLabels(service, component string) map[string]string {
	labels := map[string]string{
		"app.kubernetes.io/managed-by": "llm-d-routerservice-operator",
		"llm-d.ai/router-service":      service,
	}
	if component != "" {
		labels["llm-d.ai/component"] = component
	}
	return labels
}

// modelServerDeployment renders the decode-only model-server
// Deployment. The engine container comes from the merged decode
// template when set; otherwise the controller synthesizes one from the
// model-server image flag (the llm-d-inference-sim stand-in) with args
// ["--model", <model.name>, "--port", "8000"].
func (r *LLMRouterServiceReconciler) modelServerDeployment(svc *llmdv1alpha1.LLMRouterService, merged *MergedSpec) *appsv1.Deployment {
	names := newChildNames(svc.Name)
	replicas := int32(1)
	var containers []corev1.Container

	if merged.Workload.Decode != nil {
		if merged.Workload.Decode.Replicas != nil {
			replicas = *merged.Workload.Decode.Replicas
		}
		if merged.Workload.Decode.Template != nil {
			for i := range merged.Workload.Decode.Template.Containers {
				containers = append(containers, *merged.Workload.Decode.Template.Containers[i].DeepCopy())
			}
		}
	}

	// First container is the model-server engine: fill image, args and
	// port defaults where the user left them unset.
	var engine corev1.Container
	if len(containers) > 0 {
		engine = containers[0]
	} else {
		containers = append(containers, corev1.Container{})
		engine = containers[0]
	}
	if engine.Name == "" {
		engine.Name = "model-server"
	}
	if engine.Image == "" {
		engine.Image = r.ModelServerImage
	}
	if len(engine.Args) == 0 {
		engine.Args = []string{"--model", svc.Spec.Model.Name, "--port", fmt.Sprint(DefaultTargetPort)}
	}
	if len(engine.Ports) == 0 {
		engine.Ports = []corev1.ContainerPort{{Name: "http", ContainerPort: DefaultTargetPort, Protocol: corev1.ProtocolTCP}}
	}
	containers[0] = engine

	// The proposal's controller behavior: inject
	// spec.workload.decode.disaggSidecar into the decode workload when
	// configured.
	if merged.Workload.Decode != nil && merged.Workload.Decode.DisaggSidecar != nil {
		sidecar := merged.Workload.Decode.DisaggSidecar.Container.DeepCopy()
		if sidecar.Name == "" {
			sidecar.Name = "disagg-sidecar"
		}
		containers = append(containers, *sidecar)
	}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      names.ModelServer,
			Namespace: svc.Namespace,
			Labels:    ownedLabels(svc.Name, ModelServerComponent),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: ownedLabels(svc.Name, ModelServerComponent)},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: ownedLabels(svc.Name, ModelServerComponent)},
				Spec: corev1.PodSpec{
					Containers: containers,
				},
			},
		},
	}
}

// eppConfig renders the merged EndpointPickerConfig document for the
// EPP's ConfigMap, plus its sha256 hash. The controller owns the
// ConfigMap; there is no --config-text or user ConfigMap ref anywhere
// in the API.
func eppConfig(config *llmdv1alpha1.EndpointPickerConfig) (yamlDoc string, hash string, err error) {
	doc := &llmdv1alpha1.EPPConfigDocument{
		APIVersion:           "llm-d.ai/v1",
		Kind:                 "EndpointPickerConfig",
		EndpointPickerConfig: *config.DeepCopy(),
	}
	yamlBytes, err := yaml.Marshal(doc)
	if err != nil {
		return "", "", fmt.Errorf("marshal epp config: %w", err)
	}
	sum := sha256.Sum256(yamlBytes)
	return string(yamlBytes), hex.EncodeToString(sum[:]), nil
}

// eppConfigMap renders the controller-owned ConfigMap the EPP mounts.
func eppConfigMap(svc *llmdv1alpha1.LLMRouterService, configYAML string) *corev1.ConfigMap {
	names := newChildNames(svc.Name)
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      names.EPPConfig,
			Namespace: svc.Namespace,
			Labels:    ownedLabels(svc.Name, EPPComponent),
		},
		Data: map[string]string{"config.yaml": configYAML},
	}
}

// eppService renders the EPP's Service: ext-proc on 9002, metrics on
// 9090. InferencePools reference it in their endpointPickerRef.
func eppService(svc *llmdv1alpha1.LLMRouterService) *corev1.Service {
	names := newChildNames(svc.Name)
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      names.EPP,
			Namespace: svc.Namespace,
			Labels:    ownedLabels(svc.Name, EPPComponent),
		},
		Spec: corev1.ServiceSpec{
			Selector: ownedLabels(svc.Name, EPPComponent),
			Ports: []corev1.ServicePort{
				{Name: "grpc", Port: EPPGRPCPort, TargetPort: intstr.FromInt32(EPPGRPCPort)},
				{Name: "metrics", Port: EPPMetricsPort, TargetPort: intstr.FromInt32(EPPMetricsPort)},
			},
		},
	}
}

// eppServiceAccount, eppRole and eppRoleBinding render the EPP's
// namespace-scoped RBAC: the EPP watches its InferencePool and the
// member pods. Without these the EPP container runs but cannot pick
// endpoints (llm-d's own chart ships the same objects).
func eppServiceAccount(svc *llmdv1alpha1.LLMRouterService) *corev1.ServiceAccount {
	names := newChildNames(svc.Name)
	return &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      names.EPP,
			Namespace: svc.Namespace,
			Labels:    ownedLabels(svc.Name, EPPComponent),
		},
	}
}

func eppRole(svc *llmdv1alpha1.LLMRouterService) *rbacv1.Role {
	names := newChildNames(svc.Name)
	return &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      names.EPP,
			Namespace: svc.Namespace,
			Labels:    ownedLabels(svc.Name, EPPComponent),
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{PoolGroup},
				Resources: []string{"inferencepools"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"pods", "services", "endpoints"},
				Verbs:     []string{"get", "list", "watch"},
			},
		},
	}
}

func eppRoleBinding(svc *llmdv1alpha1.LLMRouterService) *rbacv1.RoleBinding {
	names := newChildNames(svc.Name)
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      names.EPP,
			Namespace: svc.Namespace,
			Labels:    ownedLabels(svc.Name, EPPComponent),
		},
		Subjects: []rbacv1.Subject{{
			Kind:      rbacv1.ServiceAccountKind,
			Name:      names.EPP,
			Namespace: svc.Namespace,
		}},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "Role",
			Name:     names.EPP,
		},
	}
}

// eppDeployment renders the EPP Deployment: image from the
// --epp-image flag, config from the controller-owned ConfigMap, pool
// wiring from deterministic names.
func (r *LLMRouterServiceReconciler) eppDeployment(svc *llmdv1alpha1.LLMRouterService, merged *MergedSpec) *appsv1.Deployment {
	names := newChildNames(svc.Name)
	replicas := int32(1)
	if merged.Routing.EPPReplicas != nil {
		replicas = *merged.Routing.EPPReplicas
	}

	args := []string{
		"--pool-name", names.Pool,
		"--pool-namespace", svc.Namespace,
		"--pool-group", PoolGroup,
		"--config-file", "/config/config.yaml",
		"--grpc-port", fmt.Sprint(EPPGRPCPort),
		"--grpc-health-port", fmt.Sprint(EPPHealthPort),
		"--v", "2",
	}

	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      names.EPP,
			Namespace: svc.Namespace,
			Labels:    ownedLabels(svc.Name, EPPComponent),
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: ownedLabels(svc.Name, EPPComponent)},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: ownedLabels(svc.Name, EPPComponent)},
				Spec: corev1.PodSpec{
					ServiceAccountName: names.EPP,
					Containers: []corev1.Container{{
						Name:  "epp",
						Image: r.EPPImage,
						Args:  args,
						Ports: []corev1.ContainerPort{
							{Name: "grpc", ContainerPort: EPPGRPCPort},
							{Name: "health", ContainerPort: EPPHealthPort},
							{Name: "metrics", ContainerPort: EPPMetricsPort},
						},
						Resources:    llmdv1alpha1.EPPDefaultResources(),
						VolumeMounts: []corev1.VolumeMount{{Name: "config", MountPath: "/config"}},
					}},
					Volumes: []corev1.Volume{{
						Name: "config",
						VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
							LocalObjectReference: corev1.LocalObjectReference{Name: names.EPPConfig},
						}},
					}},
				},
			},
		},
	}
}

// inferencePool renders the InferencePool: the selector is derived
// from the model-server Deployment's own selector, the
// endpointPickerRef points at the EPP Service, and the target ports
// come from routing.inferencePool.spec.targetPorts (or 8000).
func inferencePool(svc *llmdv1alpha1.LLMRouterService, merged *MergedSpec, workloadSelector map[string]string) *igwapi.InferencePool {
	names := newChildNames(svc.Name)

	targetPorts := merged.Routing.PoolTargetPorts
	poolPorts := make([]igwapi.Port, 0, len(targetPorts))
	for _, tp := range targetPorts {
		poolPorts = append(poolPorts, igwapi.Port{Number: igwapi.PortNumber(tp.Number)})
	}
	if len(poolPorts) == 0 {
		poolPorts = []igwapi.Port{{Number: igwapi.PortNumber(DefaultTargetPort)}}
	}

	selector := make(map[igwapi.LabelKey]igwapi.LabelValue, len(workloadSelector))
	for k, v := range workloadSelector {
		selector[igwapi.LabelKey(k)] = igwapi.LabelValue(v)
	}

	return &igwapi.InferencePool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      names.Pool,
			Namespace: svc.Namespace,
			Labels:    ownedLabels(svc.Name, ""),
		},
		Spec: igwapi.InferencePoolSpec{
			Selector:    igwapi.LabelSelector{MatchLabels: selector},
			TargetPorts: poolPorts,
			EndpointPickerRef: &igwapi.EndpointPickerRef{
				Group:       ptr.To(igwapi.Group("")),
				Kind:        igwapi.Kind("Service"),
				Name:        igwapi.ObjectName(names.EPP),
				Port:        &igwapi.Port{Number: igwapi.PortNumber(EPPGRPCPort)},
				FailureMode: igwapi.EndpointPickerFailOpen,
			},
		},
	}
}

// httpRoute renders the HTTPRoute: parentRef from
// routing.gateway.refs[0], cross-group backendRef to the
// InferencePool. The controller never creates the Gateway.
func httpRoute(svc *llmdv1alpha1.LLMRouterService, merged *MergedSpec, gatewayName string) *gwapiv1.HTTPRoute {
	names := newChildNames(svc.Name)
	route := &gwapiv1.HTTPRoute{
		ObjectMeta: metav1.ObjectMeta{
			Name:      names.Route,
			Namespace: svc.Namespace,
			Labels:    ownedLabels(svc.Name, ""),
		},
		Spec: gwapiv1.HTTPRouteSpec{
			CommonRouteSpec: gwapiv1.CommonRouteSpec{
				ParentRefs: []gwapiv1.ParentReference{{
					Group: ptr.To(gwapiv1.Group("gateway.networking.k8s.io")),
					Kind:  ptr.To(gwapiv1.Kind("Gateway")),
					Name:  gwapiv1.ObjectName(gatewayName),
				}},
			},
			Rules: []gwapiv1.HTTPRouteRule{{
				BackendRefs: []gwapiv1.HTTPBackendRef{{
					BackendRef: gwapiv1.BackendRef{
						BackendObjectReference: gwapiv1.BackendObjectReference{
							Group: ptr.To(gwapiv1.Group(PoolGroup)),
							Kind:  ptr.To(gwapiv1.Kind("InferencePool")),
							Name:  gwapiv1.ObjectName(names.Pool),
						},
						Weight: ptr.To(int32(1)),
					},
				}},
			}},
		},
	}
	if len(merged.Routing.Hostnames) > 0 {
		route.Spec.Hostnames = make([]gwapiv1.Hostname, 0, len(merged.Routing.Hostnames))
		for _, h := range merged.Routing.Hostnames {
			route.Spec.Hostnames = append(route.Spec.Hostnames, gwapiv1.Hostname(h))
		}
	}
	return route
}

// jsonRaw builds an apiextensionsv1.JSON from a raw JSON literal.
func jsonRaw(s string) *apiextensionsv1.JSON {
	return &apiextensionsv1.JSON{Raw: []byte(s)}
}

// defaultEPPConfig mirrors the llm-d chart's default-plugins.yaml
// profile: the operator's default EndpointPickerConfig when neither
// the service nor the profile sets one.
func defaultEPPConfig() *llmdv1alpha1.EndpointPickerConfig {
	return &llmdv1alpha1.EndpointPickerConfig{
		Plugins: []llmdv1alpha1.PluginSpec{
			{Type: "queue-scorer"},
			{Type: "kv-cache-utilization-scorer"},
			{Type: "prefix-cache-scorer"},
			{Type: "metrics-data-source", Parameters: jsonRaw(`{"scheme":"http","path":"/metrics","insecureSkipVerify":true}`)},
			{Type: "core-metrics-extractor"},
		},
		SchedulingProfiles: []llmdv1alpha1.SchedulingProfile{{
			Name: "default",
			Plugins: []llmdv1alpha1.SchedulingPlugin{
				{PluginRef: "queue-scorer", Weight: ptr.To(2.0)},
				{PluginRef: "kv-cache-utilization-scorer", Weight: ptr.To(2.0)},
				{PluginRef: "prefix-cache-scorer", Weight: ptr.To(3.0)},
			},
		}},
	}
}
