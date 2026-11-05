#!/usr/bin/env bash
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.


# Installs or removes an agentsession baseline: pod (one Pod per session) or
# warmpool (an Agent Sandbox warm pool). Run benchmarking/locust/deploy.sh
# with the same --sandbox-backend afterwards. --sweep deletes the sandboxes
# a run left behind; run it between runs.

set -o errexit -o nounset -o pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
MANIFESTS="${SCRIPT_DIR}/manifests"
# Matches sandbox.SessionLabel in internal/benchmarking/boomer/sandbox.
SESSION_LABEL="agentsession.ate.dev/session"
AGENT_SANDBOX_VERSION="v1.0.9"
AGENT_SANDBOX_URL="https://github.com/kubernetes-sigs/agent-sandbox/releases/download/${AGENT_SANDBOX_VERSION}/sandbox-with-extensions.yaml"

export ACTOR_CPU_MILLICORES="${ACTOR_CPU_MILLICORES:-1000}"
export ACTOR_MEMORY_BYTES="${ACTOR_MEMORY_BYTES:-1073741824}"
export SANDBOX_RUNTIME_CLASS="${SANDBOX_RUNTIME_CLASS-gvisor}"
export WARMPOOL_REPLICAS="${WARMPOOL_REPLICAS:-50}"

usage() {
  echo "Usage: $0 --deploy|--delete --backend pod|warmpool"
  echo "       $0 --sweep"
  echo ""
  echo "Env: KO_DOCKER_REPO (required for --deploy), ACTOR_CPU_MILLICORES, ACTOR_MEMORY_BYTES,"
  echo "     SANDBOX_RUNTIME_CLASS (default gvisor; empty omits runtimeClassName), WARMPOOL_REPLICAS"
}

# pod_spec prints the shared pod spec, indented by $1 spaces.
pod_spec() {
  local indent
  indent="$(printf '%*s' "$1" '')"
  envsubst < "${MANIFESTS}/pod-spec.yaml.tmpl" \
    | if [[ -z "${SANDBOX_RUNTIME_CLASS}" ]]; then sed '/^runtimeClassName:/d'; else cat; fi \
    | sed "s/^/${indent}/"
}

# ko_resolve builds the ko:// images on stdin and replaces the references.
ko_resolve() {
  (cd "${REPO_ROOT}" && hack/run-tool.sh ko resolve -f -)
}

render_pod() {
  cat <<YAML
apiVersion: v1
kind: Pod
metadata:
  labels:
    app: agentsession-sandbox
spec:
$(pod_spec 2)
YAML
}

render_warmpool() {
  cat <<YAML
apiVersion: extensions.agents.x-k8s.io/v1beta1
kind: SandboxTemplate
metadata:
  name: glutton
  namespace: benchmark-sandboxes
spec:
  # Managed mode adds a NetworkPolicy that would block the boomer workers.
  networkPolicyManagement: Unmanaged
  podTemplate:
    metadata:
      labels:
        app: agentsession-sandbox
    spec:
$(pod_spec 6)
---
apiVersion: extensions.agents.x-k8s.io/v1beta1
kind: SandboxWarmPool
metadata:
  name: glutton-pool
  namespace: benchmark-sandboxes
spec:
  replicas: ${WARMPOOL_REPLICAS}
  sandboxTemplateRef:
    name: glutton
  # Replace ready sandboxes when the template changes, so no claim gets a
  # sandbox built from an older one.
  updateStrategy:
    type: Recreate
YAML
}

# sweep deletes the Pods and SandboxClaims earlier runs left behind, for
# example after a worker was killed before its shutdown sweep.
sweep() {
  kubectl get namespace benchmark-sandboxes >/dev/null 2>&1 || return 0
  kubectl -n benchmark-sandboxes delete pods -l "${SESSION_LABEL}" --ignore-not-found --wait=true
  if kubectl get crd sandboxclaims.extensions.agents.x-k8s.io >/dev/null 2>&1; then
    kubectl -n benchmark-sandboxes delete sandboxclaims -l "${SESSION_LABEL}" --ignore-not-found --wait=true
  fi
}

deploy() {
  : "${KO_DOCKER_REPO:?KO_DOCKER_REPO must be set}"
  # pod_spec runs in a command substitution, where a missing tool would
  # leave an empty spec instead of stopping the script.
  local tool
  for tool in envsubst kubectl; do
    command -v "${tool}" >/dev/null || { echo "${tool} not found in PATH" >&2; exit 1; }
  done
  sweep
  kubectl apply -f "${MANIFESTS}/namespace-rbac.yaml"
  kubectl create namespace benchmarking --dry-run=client -o yaml | kubectl apply -f -
  case "$1" in
    pod)
      render_pod | ko_resolve > "${TMPDIR:-/tmp}/sandbox-pod.yaml"
      kubectl -n benchmarking create configmap sandbox-pod-template \
        --from-file=pod.yaml="${TMPDIR:-/tmp}/sandbox-pod.yaml" --dry-run=client -o yaml | kubectl apply -f -
      # Workers read the template once at start; restart them to pick it up.
      if kubectl -n benchmarking get deployment locust >/dev/null 2>&1; then
        kubectl -n benchmarking rollout restart deployment/locust
      fi
      ;;
    warmpool)
      kubectl apply --server-side -f "${AGENT_SANDBOX_URL}"
      kubectl wait --for=condition=Established --timeout=120s \
        crd/sandboxwarmpools.extensions.agents.x-k8s.io crd/sandboxtemplates.extensions.agents.x-k8s.io \
        crd/sandboxclaims.extensions.agents.x-k8s.io
      render_warmpool | ko_resolve | kubectl apply -f -
      kubectl -n benchmark-sandboxes wait --for=jsonpath='{.status.readyReplicas}'="${WARMPOOL_REPLICAS}" \
        sandboxwarmpool/glutton-pool --timeout=600s
      ;;
    *) usage; exit 1 ;;
  esac
}

delete() {
  sweep
  kubectl -n benchmarking delete configmap sandbox-pod-template --ignore-not-found
  if [[ "$1" == "warmpool" ]] && kubectl get crd sandboxclaims.extensions.agents.x-k8s.io >/dev/null 2>&1; then
    kubectl -n benchmark-sandboxes delete sandboxclaims --all --ignore-not-found
    kubectl -n benchmark-sandboxes delete sandboxwarmpool glutton-pool --ignore-not-found
    kubectl -n benchmark-sandboxes delete sandboxtemplate glutton --ignore-not-found
  fi
  kubectl delete namespace benchmark-sandboxes --ignore-not-found
}

action=""
backend=""
while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --deploy) action="deploy" ;;
    --delete) action="delete" ;;
    --sweep) action="sweep" ;;
    --backend) shift; backend="${1:-}" ;;
    -h|--help) usage; exit 0 ;;
    *) usage; exit 1 ;;
  esac
  shift
done
if [[ "${action}" == "sweep" ]]; then
  sweep
  exit 0
fi
if [[ -z "${action}" || -z "${backend}" ]]; then
  usage
  exit 1
fi
"${action}" "${backend}"
