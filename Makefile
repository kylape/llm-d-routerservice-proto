# llm-d-routerservice-proto build helpers.
# NOTE: always invoke go through `env -u GOROOT` (stale GOROOT in this
# environment breaks plain go).

CONTROLLER_GEN := env -u GOROOT go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.17.3

.PHONY: all build test manifests generate fmt vet clean

all: build

build:
	env -u GOROOT go build -o bin/manager ./cmd

test:
	env -u GOROOT go test ./...

## manifests: generate CRD manifests and RBAC into config/
manifests:
	$(CONTROLLER_GEN) crd:allowDangerousTypes=true paths=./api/... output:crd:artifacts:config=config/crd/bases
	$(CONTROLLER_GEN) rbac:roleName=manager-role paths=./internal/controller/... output:rbac:artifacts:config=config/rbac

## generate: regenerate deepcopy for the API types
generate:
	$(CONTROLLER_GEN) object paths=./api/...

fmt:
	env -u GOROOT go fmt ./...

vet:
	env -u GOROOT go vet ./...

clean:
	rm -rf bin/
