#!/usr/bin/env bash
# Spins up the full shipmate demo on a local MicroK8s: builds the operator
# image, sideloads it into containerd (no registry required), deploys the
# CRD + controller, and applies a demo App.
#
# Prereqs: microk8s (on macOS: `brew install ubuntu/microk8s/microk8s &&
# microk8s install`), docker, make. Run from the repo root.
set -euo pipefail

IMG=${IMG:-shipmate:dev}

echo "==> Waiting for MicroK8s"
microk8s status --wait-ready >/dev/null

echo "==> Enabling addons (dns, ingress)"
microk8s enable dns ingress

echo "==> Pointing kubectl at MicroK8s"
export KUBECONFIG="$(mktemp)"
microk8s config > "$KUBECONFIG"

echo "==> Building operator image ${IMG}"
make docker-build IMG="${IMG}"

echo "==> Sideloading image into MicroK8s containerd"
docker save "${IMG}" | microk8s images import -

echo "==> Installing CRD and deploying controller"
make install deploy IMG="${IMG}"
kubectl -n shipmate-system rollout status deploy/shipmate-controller-manager --timeout=120s

echo "==> Applying demo App"
kubectl apply -f - <<'EOF'
apiVersion: shipmate.florya.co/v1alpha1
kind: App
metadata:
  name: hello
spec:
  image: nginxdemos/hello:0.4
  port: 80
  replicas: 2
  domain: hello.local
EOF

echo "==> Waiting for the app to come up"
kubectl wait app/hello --for=condition=Available --timeout=120s

VM_IP=$(kubectl get nodes -o jsonpath='{.items[0].status.addresses[?(@.type=="InternalIP")].address}')
echo
kubectl get app hello
echo
echo "Demo up. To browse it, map the host and open http://hello.local:"
echo "  echo '${VM_IP} hello.local' | sudo tee -a /etc/hosts"
echo
echo "Things to try:"
echo "  kubectl scale is not needed — edit the CR instead:"
echo "    kubectl patch app hello --type merge -p '{\"spec\":{\"replicas\":4}}'"
echo "  Unexpose it (the operator deletes the Ingress):"
echo "    kubectl patch app hello --type merge -p '{\"spec\":{\"domain\":\"\"}}'"
echo "  Break something and watch it heal:"
echo "    kubectl delete deployment hello   # recreated within seconds"
echo "  Tear down:"
echo "    kubectl delete app hello          # children garbage-collected"
