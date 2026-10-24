#!/usr/bin/env bash
# Spin up a kind cluster with 3×v1 and 1×v2 behind one Service, using a
# locally built image. No registry, no controller: just the grid.
set -euo pipefail
cd "$(dirname "$0")/.."

CLUSTER=${CLUSTER:-canarybird}
IMAGE=${IMAGE:-canarybird:dev}

command -v kind >/dev/null || { echo "kind is required: https://kind.sigs.k8s.io"; exit 1; }
command -v kubectl >/dev/null || { echo "kubectl is required"; exit 1; }

if ! kind get clusters | grep -qx "$CLUSTER"; then
  kind create cluster --name "$CLUSTER" --wait 60s
fi

docker build -t "$IMAGE" --build-arg VERSION=dev .
kind load docker-image "$IMAGE" --name "$CLUSTER"

sed "s|ghcr.io/thiha-min-thant/canarybird:latest|$IMAGE|g; s|imagePullPolicy: .*||" examples/quickstart.yaml \
  | kubectl --context "kind-$CLUSTER" apply -f -
kubectl --context "kind-$CLUSTER" set image deploy/canarybird-v1 canarybird="$IMAGE" >/dev/null
kubectl --context "kind-$CLUSTER" set image deploy/canarybird-v2 canarybird="$IMAGE" >/dev/null
kubectl --context "kind-$CLUSTER" rollout status deploy/canarybird-v1 --timeout=120s
kubectl --context "kind-$CLUSTER" rollout status deploy/canarybird-v2 --timeout=120s

cat <<EOF

canarybird is up: 3 × v1 (blue) and 1 × v2 (green) behind svc/canarybird.

  kubectl --context kind-$CLUSTER port-forward svc/canarybird 8080:80
  open http://localhost:8080

Try:
  Traffic → Expected split: v1=75,v2=25
  Break something → Apply to: v2 only → error-spike
  kubectl --context kind-$CLUSTER scale deploy/canarybird-v2 --replicas=3   # watch the split move to 50/50
EOF
