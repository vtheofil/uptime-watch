# Everyday commands. `make help` lists them.
SHELL := /bin/bash
IMAGE ?= uptime-watch:dev
KIND_CLUSTER ?= uptime-watch
# The kind cluster gets its own kubeconfig file, so your normal kubectl
# context (e.g. work clusters) is never touched.
export KUBECONFIG := $(CURDIR)/.kube/kind-$(KIND_CLUSTER).yaml
DATABASE_URL ?= postgres://uptime:uptime@localhost:5433/uptime?sslmode=disable

.PHONY: help
help: ## list the targets
	@grep -E '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-22s %s\n", $$1, $$2}'

# ---- code ----

.PHONY: fmt
fmt: ## format the code
	gofmt -w .

# The Go version from go.mod, so tools run with `go run x@latest` use it too.
GO_TOOLCHAIN := go$(shell awk '/^go /{print $$2}' go.mod)

.PHONY: lint
lint: ## gofmt, go vet, staticcheck and govulncheck (the same checks as CI)
	@test -z "$$(gofmt -l .)" || { gofmt -l .; echo "run make fmt"; exit 1; }
	go vet ./...
	GOTOOLCHAIN=$(GO_TOOLCHAIN) go run honnef.co/go/tools/cmd/staticcheck@latest ./...
	GOTOOLCHAIN=$(GO_TOOLCHAIN) go run golang.org/x/vuln/cmd/govulncheck@latest ./...

.PHONY: test
test: ## run all tests (needs Docker for the Postgres tests)
	go test -race ./...

.PHONY: test-rules
test-rules: ## unit-test the alert rules with promtool (needs helm and Docker)
	./deploy/helm/tests/test-rules.sh

.PHONY: build
build: ## build bin/uptime-watch
	go build -o bin/uptime-watch ./cmd/uptime-watch

# ---- local run ----

.PHONY: db
db: ## start Postgres in Docker and apply migrations
	docker compose up -d --wait
	DATABASE_URL='$(DATABASE_URL)' go run ./cmd/uptime-watch migrate

.PHONY: run
run: build ## run serve (:8080) and worker (:8081) against the local Postgres; Ctrl+C stops both
	@trap 'kill 0' EXIT; \
	DATABASE_URL='$(DATABASE_URL)' ADMIN_TOKEN=$${ADMIN_TOKEN:-dev-token} ./bin/uptime-watch serve & \
	DATABASE_URL='$(DATABASE_URL)' PORT=8081 ./bin/uptime-watch worker & \
	wait

# ---- container image ----

.PHONY: image
image: ## build the container image
	docker build -t $(IMAGE) --build-arg VERSION=$$(git describe --always --dirty) .

# ---- Kubernetes on kind (M7) ----

.PHONY: kind-up
kind-up: ## create the kind cluster (own kubeconfig in .kube/)
	@mkdir -p .kube
	kind get clusters | grep -qx $(KIND_CLUSTER) || kind create cluster --name $(KIND_CLUSTER) --config deploy/kind/cluster.yaml
	kubectl cluster-info

.PHONY: kind-down
kind-down: ## delete the kind cluster
	kind delete cluster --name $(KIND_CLUSTER)
	rm -f $(KUBECONFIG)

.PHONY: kind-deploy
kind-deploy: image ## build the image, load it into kind, install Postgres and the chart
	kind load docker-image $(IMAGE) --name $(KIND_CLUSTER)
	./deploy/kind/e2e.sh $(IMAGE)

.PHONY: kind-monitoring
kind-monitoring: ## install Prometheus, Grafana and Alertmanager into kind (run before kind-deploy)
	./deploy/monitoring/install.sh

.PHONY: kind-argocd
kind-argocd: ## install Argo CD and let it deploy everything from GitHub (use a fresh cluster)
	./deploy/argocd/bootstrap.sh

.PHONY: kind-ui
kind-ui: ## open the status page on http://localhost:8080 (Ctrl+C to stop)
	kubectl -n uptime-watch port-forward svc/uptime-watch 8080:80

# make always uses the kind kubeconfig above, whatever your terminal has set,
# so these targets can never reach another cluster by mistake.
.PHONY: kind-ports
kind-ports: ## open every UI of the kind cluster on localhost (Ctrl+C closes them all)
	@echo "cluster: $$(kubectl config current-context)"
	@trap 'kill 0' EXIT; \
	kubectl -n uptime-watch port-forward svc/uptime-watch 18080:80 >/dev/null & \
	kubectl -n monitoring port-forward svc/monitoring-kube-prometheus-prometheus 9090:9090 >/dev/null & \
	kubectl -n monitoring port-forward svc/monitoring-grafana 3000:80 >/dev/null & \
	kubectl -n monitoring port-forward svc/monitoring-kube-prometheus-alertmanager 9093:9093 >/dev/null & \
	kubectl -n argocd port-forward svc/argocd-server 8443:80 >/dev/null & \
	sleep 2; \
	echo "status page   http://localhost:18080"; \
	echo "Prometheus    http://localhost:9090"; \
	echo "Grafana       http://localhost:3000   (admin; password admin on first login)"; \
	echo "Alertmanager  http://localhost:9093"; \
	echo "Argo CD       http://localhost:8443   (admin / make kind-argocd-password)"; \
	echo "Ctrl+C closes them all"; \
	wait

# With Argo CD, Traefik serves every UI on localhost:8000, routed by host name
# (*.localhost names point at this machine). No port-forward, so a rollout
# does not break them.
.PHONY: kind-urls
kind-urls: ## list the UIs served through Traefik (after make kind-argocd)
	@echo "status page   http://status.localhost:8000"
	@echo "Prometheus    http://prometheus.localhost:8000"
	@echo "Grafana       http://grafana.localhost:8000   (admin; password admin on first login)"
	@echo "Alertmanager  http://alertmanager.localhost:8000"
	@echo "Argo CD       http://argocd.localhost:8000    (admin / make kind-argocd-password)"

.PHONY: kind-argocd-password
kind-argocd-password: ## print the Argo CD admin password
	@kubectl -n argocd get secret argocd-initial-admin-secret -o jsonpath='{.data.password}' | base64 -d; echo

.PHONY: kind-discord-secret
kind-discord-secret: ## store your Discord webhook URL in the cluster (asked for, never shown or saved)
	@read -rsp "Paste the Discord webhook URL (hidden), then Enter: " url; echo; \
	case "$$url" in https://discord.com/api/webhooks/*) ;; *) echo "that does not look like a Discord webhook URL"; exit 1;; esac; \
	kubectl create namespace monitoring --dry-run=client -o yaml | kubectl apply -f - >/dev/null; \
	printf '%s' "$$url" | kubectl -n monitoring create secret generic alertmanager-discord \
	  --from-file=webhook-url=/dev/stdin --dry-run=client -o yaml | kubectl apply -f -

.PHONY: kind-token
kind-token: ## print the ADMIN_TOKEN of the app in kind
	@kubectl -n uptime-watch get secret uptime-watch -o jsonpath='{.data.ADMIN_TOKEN}' | base64 -d; echo

# ---- secrets: OpenBao outside kind, External Secrets inside ----

.PHONY: openbao-up
openbao-up: ## start OpenBao next to kind, initialise it once and unseal it (after kind-up)
	./deploy/openbao/setup.sh

.PHONY: openbao-down
openbao-down: ## stop OpenBao (the secrets stay in its Docker volume)
	docker compose -p uptime-watch-openbao -f deploy/openbao/compose.yaml down

.PHONY: openbao-discord
openbao-discord: ## store the Discord webhook URL in OpenBao (asked for, never shown or saved)
	./deploy/openbao/put-discord.sh

.PHONY: kind-openbao-token
kind-openbao-token: ## give External Secrets in kind a read-only OpenBao token (bootstrap does this)
	./deploy/openbao/k8s-token.sh

.PHONY: kind-refresh-secrets
kind-refresh-secrets: ## fetch the secrets from OpenBao now instead of at the next refresh
	kubectl -n monitoring annotate externalsecret alertmanager-discord force-sync=$$(date +%s) --overwrite
