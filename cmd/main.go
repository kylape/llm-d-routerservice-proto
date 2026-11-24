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

// The llm-d-routerservice manager runs the LLMRouterService and
// LLMRouterServiceProfile controllers: one LLMRouterService object
// renders into the complete serving stack (model-server Deployment,
// llm-d-router EPP, InferencePool, Gateway API routing).
package main

import (
	"flag"
	"os"

	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	llmdv1alpha1 "github.com/llm-d/llm-d-routerservice-proto/api/v1alpha1"
	"github.com/llm-d/llm-d-routerservice-proto/internal/controller"
	igwapi "sigs.k8s.io/gateway-api-inference-extension/api/v1"
	gwapiv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// Default images, pinned per operator release.
const (
	defaultModelServerImage = "ghcr.io/llm-d/llm-d-inference-sim:v0.11.2"
	defaultEPPImage         = "ghcr.io/llm-d/llm-d-router-endpoint-picker:v1.0.0"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(gwapiv1.Install(scheme))
	utilruntime.Must(igwapi.Install(scheme))
	utilruntime.Must(llmdv1alpha1.AddToScheme(scheme))
}

func main() {
	var metricsAddr string
	var probeAddr string
	var leaderElect bool
	var modelServerImage string
	var eppImage string
	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "address the metrics endpoint binds to")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "address the probe endpoint binds to")
	flag.BoolVar(&leaderElect, "leader-elect", false, "enable leader election for the manager")
	flag.StringVar(&modelServerImage, "model-server-image", defaultModelServerImage,
		"model-server image used when spec.workload.decode.template sets none (the llm-d-inference-sim stand-in)")
	flag.StringVar(&eppImage, "epp-image", defaultEPPImage,
		"llm-d-router Endpoint Picker image")
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseDevMode(false)))
	setupLog := ctrl.Log.WithName("setup")

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: metricsAddr},
		HealthProbeBindAddress: probeAddr,
		LeaderElection:         leaderElect,
		LeaderElectionID:       "llm-d-routerservice-operator.llm-d.ai",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	if err := (&controller.LLMRouterServiceReconciler{
		Client:           mgr.GetClient(),
		Scheme:           mgr.GetScheme(),
		ModelServerImage: modelServerImage,
		EPPImage:         eppImage,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "LLMRouterService")
		os.Exit(1)
	}

	if err := (&controller.LLMRouterServiceProfileReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "LLMRouterServiceProfile")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "manager exited")
		os.Exit(1)
	}
}
