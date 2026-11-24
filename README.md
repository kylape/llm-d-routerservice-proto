# llm-d-routerservice-proto

A Kubernetes operator prototype implementing the
**LLMRouterService proposal**: one user-facing CRD turns into the
complete llm-d serving stack — model-server workload, llm-d-router
Endpoint Picker (EPP), InferencePool and Gateway API routing — with
reusable profiles merged underneath and pinned for upgrade safety.

This repo exists to evaluate the proposal's API shape and its
distinctive mechanics on their own merits. It is a v0 prototype: the
simplifications below are deliberate and documented, never silent.

## The CRDs

Both resources live in one flat group, `llm-d.ai/v1alpha1`, exactly
as the proposal specifies.

* **LLMRouterService** — the user-facing service. Owns model identity
  (`spec.model.{uri,name}`), the workload topology
  (`spec.workload.{kind,prefill,decode,encode,worker}` with
  `tp`/`pp`/`dp` parallelism field names kept verbatim), and routing
  (`spec.routing.{mode,gateway.refs,httpRoute,router.epp,inferencePool}`).
* **LLMRouterServiceProfile** — the reusable preset. Owns
  infrastructure defaults only: workload kind/templates, routing mode,
  gateway refs, EPP replicas and the default
  `EndpointPickerConfig`, and InferencePool target ports.

Per the proposal's field-ownership table, the profile never owns model
identity, role-specific values or replicas — it only supplies
*defaults*; the service wins everywhere.

## What one service renders

From the merged spec (service ⊕ pinned profile content):

* a **model-server Deployment** (`<name>-model-server`) — decode-only,
  engine container from `workload.decode.template.containers[0]` or
  synthesized from the `--model-server-image` flag (the
  `llm-d-inference-sim` stand-in) with args
  `["--model", <model.name>, "--port", "8000"]`; `decode.disaggSidecar`
  is injected as an extra container when set
* an **EPP ConfigMap** (`<name>-epp-config`) — the merged
  `EndpointPickerConfig` document, controller-owned and mounted at
  `/config/config.yaml` (`--config-file`)
* an **EPP Deployment + Service** (`<name>-epp`) — image from the
  `--epp-image` flag, args `--pool-name/--pool-namespace/--pool-group
  inference.networking.k8s.io --config-file /config/config.yaml
  --grpc-port 9002`, ports 9002 (ext-proc) / 9090 (metrics)
* an **EPP ServiceAccount + Role + RoleBinding** — an addition beyond
  the proposal's child list, mirroring the llm-d chart: without it the
  EPP container runs but cannot watch its InferencePool or member pods
  (verified live: the default ServiceAccount gets `forbidden` on
  `inferencepools` and `pods` list)
* an **InferencePool** (`<name>`) — selector **derived from the
  model-server Deployment's own selector**, `endpointPickerRef` → the
  EPP Service (port 9002, `FailureMode: FailOpen`), `targetPorts` from
  `routing.inferencePool.spec.targetPorts` or 8000
* an **HTTPRoute** (`<name>`) — `parentRef` from
  `routing.gateway.refs[0].name`, cross-group `backendRef` to the
  InferencePool (`group: inference.networking.k8s.io`, no port — the
  GAIE/llm-d-canonical shape)

The controller **never creates the Gateway**. Children render on
*existence* of inputs; readiness is reported through conditions only
— child rendering is never gated on a parent's Ready condition,
which would deadlock the wiring.

## The distinctive mechanics

### Profile merge

`internal/controller/merge.go` deep-merges the profile's defaults
under the service spec: the service wins everywhere it sets a field,
the profile fills unset ones. Structs merge field-wise (including
`parallelism.{tp,pp,dp}` subfields); lists replace wholesale (a
non-empty service list wins). The `EndpointPickerConfig` merges
field-wise at the document's top level: the service's
`endpointPicker.inline` `plugins` win while the profile's
`schedulingProfiles` fill in, and vice versa. Precedence cases are
table-tested in `merge_test.go`, including a no-aliasing guard.

The merge normalizes the proposal's field-path asymmetry (see
*Interpretations* below) into single slots
(`MergedSpec.Routing.EPPConfig` etc.).

### Profile pinning (resolvedProfileRef)

On the first successful reconcile the controller records the profile's
name and `metadata.generation` in `status.resolvedProfileRef`. On later
reconciles, if the referenced profile's generation differs from the
pin, the controller **does not re-render children from the new
content**:

* children are left untouched (Deployment specs, EPP ConfigMap, pool,
  route — everything frozen as last rendered)
* `status` fields that describe those children (observedGeneration,
  the pin, `endpointPickerConfigHash`, workload/routing conditions)
  are carried forward unchanged
* `ProfilesReady=False` with reason `ProfileDrift` and a message
  explaining how to accept

The proposal says a service picks up changed profile content when the
user "updates spec.profileRef, or bumps the profile revision
referenced by spec.profileRef". Changing `spec.profileRef` (including
re-pointing to another profile) re-pins immediately — that path is
implemented literally. For "bumps the revision" this prototype
provides the accept annotation:

```sh
kubectl annotate llmrouterservice/sim-qwen llm-d.ai/accept-profile-drift=true
```

Setting `llm-d.ai/accept-profile-drift: "true"` re-pins the service to
the profile's *current* generation, applies it (children re-render),
and the controller **removes the annotation** — acceptance is
explicit, one-shot per drift event, so a future drift never applies
silently.

Note the deliberate consequence: while drift is unaccepted, *all*
rendering is frozen, including changes to the service's own spec. The
controller cannot merge the new service spec with the pinned profile
content because the pin records only name+generation, not content (the
proposal does not define a content snapshot). Accept the drift or
revert the profile to unblock.

Missing or terminating profiles hold the same way
(`ProfilesReady=False/ProfileNotFound` / `ProfileTerminating`,
children untouched) — deleting a profile never deletes or mutates the
serving stack.

### status.endpointPickerConfigHash

The sha256 (full hex) of the merged `EndpointPickerConfig` document
exactly as rendered into the EPP ConfigMap — the effective
configuration is observable without exposing the full object in
status. During a drift hold the hash stays at the frozen render's
value.

### Config ownership

The merged EPP config is rendered into a controller-owned ConfigMap
and mounted with `--config-file`. There is no `--config-text` and no
user ConfigMap reference anywhere in the API, per the proposal's
configuration-ownership rule. When neither service nor profile sets a
config, the operator default applies (mirroring the llm-d chart's
`default-plugins.yaml`: queue/kv-cache-utilization/prefix-cache
scorers, metrics-data-source, core-metrics-extractor, default
scheduling profile). Validation of the config document happens at CRD
admission (`plugins` and `schedulingProfiles` are required fields);
the controller does not re-validate.

## Status

LLMRouterService reports the proposal's v0 subset:
`observedGeneration`, `Ready`, `WorkloadsReady`, `RoutingReady`,
`ProfilesReady`, `resolvedProfileRef`, `endpointPickerConfigHash`.
`Ready` aggregates the three sub-conditions in that order, surfacing
the first failing sub-condition's reason and message.

LLMRouterServiceProfile reports `observedGeneration`, `Ready` and
`ReferencedBy` (same-namespace services referencing it via
`spec.profileRef`). A profile renders nothing itself.

## Documented v0 simplifications

Everything in this list is a scope decision, not a spec change; the
proposal's field shapes are kept wherever they appear.

* **LLMBatchGateway is not implemented.** The CRD already exists as
  `batch.llm-d.ai` in
  [opendatahub-io/llm-d-batch-gateway-operator](https://github.com/opendatahub-io/llm-d-batch-gateway-operator);
  re-homing it into `llm-d.ai` is out of prototype scope.
* **Workload: decode-only, `spec.workload.kind=Deployment` only.** The
  `prefill`, `encode` and `worker` role fields (and `disaggSidecar`)
  are present in the schema, and `kind` carries the proposal's enum
  (`Deployment`, `DisaggregatedSet`, `LeaderWorkerSet`), but the
  controller rejects non-decode topologies with
  `WorkloadsReady=False` reason `UnsupportedTopology` and renders
  nothing (existing children are left untouched — rejection freezes,
  it never deletes). `parallelism` keeps the proposal's `tp`/`pp`/`dp`
  names; it is stored, not translated into engine flags in v0.
  `kvCacheOffloading` and `rolloutStrategy` are schema-reserved
  (preserved-unknown-fields JSON), stored but not interpreted.
  `decode.disaggSidecar` *is* honored (injected as an extra
  container), per the proposal's controller behavior.
* **Routing: `mode: gateway` only.** `standalone` yields
  `RoutingReady=False` reason `UnsupportedMode` — the workload still
  renders, the routing stack is held. `coordinator`, `proxy` and
  `tracing` are omitted from the schema entirely (the proposal keeps
  them for standalone/E-P-D pipelines, both out of v0 scope).
* **Model: `{uri, name}` only.** `lora`, `confidential` and
  `storageInitializer` are noted as future work; v0 has no model
  download (the sim stands in).
* **Model-server image**: from `spec.workload.decode.template`'s first
  container when set, else the `--model-server-image` flag (default
  `ghcr.io/llm-d/llm-d-inference-sim:v0.11.2`) with args
  `["--model", <model.name>, "--port", "8000"]` — the sim is the demo
  stand-in, documented as such. A real engine arrives entirely through
  the decode template (image + args pass through).
* **EPP image**: the `--epp-image` flag (default
  `ghcr.io/llm-d/llm-d-router-endpoint-picker:v1.0.0`). The proposal's
  `routing.router.epp.template` (image/resource overrides) is not in
  the v0 schema; only `replicas` and `endpointPicker.inline` are.
* **HTTPRoute**: `routing.httpRoute` carries `hostnames` only; v0
  always renders one catch-all rule. Only `gateway.refs[0]` is used
  for the parentRef.
* **Status omissions**: the proposal's `URL`, `Workloads`, `Routes`
  and `InferencePool` status refs are not implemented; conditions plus
  the pin and hash are.
* **Operator upgrade rule**: the proposal's "existing services never
  change on operator upgrade" holds trivially in v0 because every
  default (images, default EPP config) is either a flag pinned at
  manager start or content addressed by the profile pin; there is no
  in-cluster default-profile CR the operator would mutate. A full
  implementation needs an explicit default-profile lifecycle.

## Interpretations (gaps the proposal left)

* **`endpointPicker.inline` vs `endpointPicker`**: the proposal's text
  puts the service's config at
  `LLMRouterService.spec.routing.router.epp.endpointPicker.inline` and
  the profile's at
  `LLMRouterServiceProfile.spec.routing.router.epp.endpointPicker`
  (no `.inline`). This looks like an inconsistency, but both paths are
  implemented verbatim; the merge normalizes them.
* **"Bumps the profile revision"**: `spec.profileRef` carries only a
  name in every example, so there is no revision field to bump. The
  accept-annotation mechanism above is this prototype's
  interpretation.
* **Rejection semantics**: the proposal never says what the controller
  does with children when it *cannot* render a spec (unsupported
  topology, missing profile). v0 freezes: children are left untouched
  and conditions explain. Nothing is deleted.
* **Pool `FailureMode`**: unset in the proposal; v0 uses `FailOpen`
  (traffic continues to some endpoint if the EPP fails) to match the
  llm-d chart's default posture.
* **EPP RBAC** (see above): the proposal's child list omits it, but
  the EPP cannot function without watch permissions on pools and
  pods, so the controller renders namespace-scoped SA/Role/RoleBinding
  like the llm-d chart does.
* **HTTPRoute backendRef port**: GAIE's and llm-d's canonical routes
  reference the InferencePool without a port; v0 does the same.

## Build, test, run

```sh
env -u GOROOT go test ./...        # fake-client suite
env -u GOROOT go build ./...       # or: make build

# regenerate deepcopy + CRDs + RBAC
env -u GOROOT go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.17.3 object paths=./api/...
env -u GOROOT go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.17.3 crd:allowDangerousTypes=true paths=./api/... output:crd:artifacts:config=config/crd/bases
env -u GOROOT go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.17.3 rbac:roleName=manager-role paths=./internal/controller/... output:rbac:artifacts:config=config/rbac
```

Deps are pinned: `sigs.k8s.io/gateway-api v1.6.2`,
`sigs.k8s.io/gateway-api-inference-extension v1.6.2`,
`controller-runtime v0.24.1`.

Run against a cluster with the GAIE
(`inferencepools.inference.networking.k8s.io`) and Gateway API CRDs
installed:

```sh
env -u GOROOT go build -o bin/manager ./cmd
# The CRDs embed the full corev1.Container schema and exceed the
# client-side apply annotation limit — apply them server-side.
kubectl apply --server-side --force-conflicts -f config/crd/bases
KUBECONFIG=<path-to-kubeconfig> ./bin/manager \
  -metrics-bind-address :8083 -health-probe-bind-address :8084 &
kubectl apply -k config/demo
kubectl get llmrouterservices -n llm-d-routerservice-demo -w
```

Convergence: `WorkloadsReady` and `RoutingReady` go True once the
model-server and EPP Deployments report Available (the Gateway is a
placeholder; conditions do not depend on a data plane).

Verified live (2026-11-24, development cluster): the demo converges to all
conditions True with the pin at generation 1; mutating the profile
bumps it to generation 2 and produces `ProfilesReady=False/
ProfileDrift` with byte-identical children; annotating
`llm-d.ai/accept-profile-drift=true` re-pins to generation 2, applies
the new EPP config (hash and ConfigMap change) and removes the
annotation; a service with `workload.prefill` set renders nothing and
reports `UnsupportedTopology`.

Try the drift mechanic:

```sh
kubectl edit llmrouterserviceprofile vllm-default -n llm-d-routerservice-demo
#   (change any spec field: generation bumps)
kubectl get llmrouterservice sim-qwen -n llm-d-routerservice-demo \
  -o jsonpath='{.status.conditions[?(@.type=="ProfilesReady")].message}'
kubectl annotate llmrouterservice sim-qwen -n llm-d-routerservice-demo \
  llm-d.ai/accept-profile-drift=true
```

## Repo layout

* `api/v1alpha1/` — the two CRDs plus the vendored
  `EndpointPickerConfig` type tree (from llm-d-router's
  `apix/config/v1alpha1`, with `apiextensionsv1.JSON` plugin
  parameters so arbitrary parameter shapes survive CRD validation)
* `internal/controller/` — merge, rendering and the two reconcilers
* `internal/kube/` — the upsert helper
* `config/crd`, `config/rbac` — generated manifests
* `config/demo/` — the kustomize demo

## Provenance

The controller patterns (EPP rendering with
`--pool-name/--pool-namespace/--pool-group` wiring, the upsert helper,
the fake-client harness, the vendored config type) follow the
llm-d-router Helm chart's conventions and were first exercised in an
earlier internal prototype; the code is re-shaped for this proposal's
API.
