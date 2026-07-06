#!/usr/bin/env bash
# Installs cert-manager, creates Let's Encrypt ClusterIssuers, and applies a
# demo App with both spec.database and spec.tls set. Assumes shipmate is
# already deployed (see hack/demo-microk8s.sh) and that the cluster's public
# IP answers on ports 80/443 — cert-manager's HTTP-01 solver needs port 80
# reachable from the internet to complete the Let's Encrypt challenge.
#
# Usage: PUBLIC_IP=1.2.3.4 ./hack/demo-database-tls.sh
set -euo pipefail

CERT_MANAGER_VERSION=${CERT_MANAGER_VERSION:-v1.20.3}
ACME_EMAIL=${ACME_EMAIL:-admin@florya.co}
PUBLIC_IP=${PUBLIC_IP:?set PUBLIC_IP to the public IP of the cluster node}
DOMAIN="db-demo.${PUBLIC_IP}.nip.io"
APP_NAME="db-demo"

echo "==> Checking for a StorageClass"
if [ -z "$(kubectl get storageclass -o name)" ]; then
  echo "No StorageClass found in this cluster — the database PVC would sit" >&2
  echo "Pending forever and the StatefulSet would never schedule. On" >&2
  echo "MicroK8s: microk8s enable hostpath-storage" >&2
  exit 1
fi

echo "==> Installing cert-manager ${CERT_MANAGER_VERSION}"
kubectl apply -f "https://github.com/cert-manager/cert-manager/releases/download/${CERT_MANAGER_VERSION}/cert-manager.yaml"

echo "==> Waiting for cert-manager to be ready (webhook included)"
kubectl wait --for=condition=Available --timeout=180s -n cert-manager \
  deployment/cert-manager deployment/cert-manager-webhook deployment/cert-manager-cainjector

echo "==> Creating Let's Encrypt ClusterIssuers (staging + prod)"
kubectl apply -f - <<EOF
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt-staging
spec:
  acme:
    server: https://acme-staging-v02.api.letsencrypt.org/directory
    email: ${ACME_EMAIL}
    privateKeySecretRef:
      name: letsencrypt-staging-account-key
    solvers:
      - http01:
          ingress:
            ingressClassName: public
---
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt-prod
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    email: ${ACME_EMAIL}
    privateKeySecretRef:
      name: letsencrypt-prod-account-key
    solvers:
      - http01:
          ingress:
            ingressClassName: public
EOF

echo "==> Applying demo App with database + tls"
kubectl apply -f - <<EOF
apiVersion: shipmate.florya.co/v1alpha1
kind: App
metadata:
  name: ${APP_NAME}
spec:
  image: nginxdemos/hello:0.4
  port: 80
  replicas: 1
  domain: ${DOMAIN}
  tls:
    clusterIssuerName: letsencrypt-prod
  database:
    storageSize: 1Gi
EOF

echo "==> Waiting for the app to come up"
kubectl wait app/${APP_NAME} --for=condition=Available --timeout=120s

echo "==> Waiting for the database StatefulSet"
kubectl wait --for=jsonpath='{.status.readyReplicas}'=1 --timeout=180s statefulset/${APP_NAME}-db

echo "==> Waiting for the certificate to be issued (can take ~30-90s for the HTTP-01 challenge)"
kubectl wait --for=condition=Ready --timeout=180s certificate/${APP_NAME}-tls

echo
kubectl get app ${APP_NAME}
echo
echo "Certificate:"
kubectl get certificate ${APP_NAME}-tls
echo
echo "Verify a real, trusted cert (no -k needed):"
echo "  curl -v https://${DOMAIN}/ 2>&1 | grep -E 'SSL certificate verify|subject:|HTTP/'"
echo
echo "Verify the database survives a pod replacement with the same credentials:"
echo "  kubectl exec ${APP_NAME}-db-0 -- psql -U ${APP_NAME} -d ${APP_NAME} -c \"CREATE TABLE proof(n int); INSERT INTO proof VALUES (42);\""
echo "  kubectl delete pod ${APP_NAME}-db-0"
echo "  kubectl wait --for=condition=Ready --timeout=60s pod/${APP_NAME}-db-0"
echo "  kubectl exec ${APP_NAME}-db-0 -- psql -U ${APP_NAME} -d ${APP_NAME} -c 'SELECT * FROM proof;'"
