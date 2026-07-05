# shipmate

A Kubernetes operator that ships your apps. One custom resource declares a
complete web application — workload, networking, scheduled backups — and a
controller keeps the cluster converged on it.

![shipmate demo: self-healing, scaling via the CR, and cascade deletion](docs/demo.gif)

*Live demo (recorded against MicroK8s on a Hetzner box):
[hello.178.105.147.131.nip.io](http://hello.178.105.147.131.nip.io)*

```yaml
apiVersion: shipmate.florya.co/v1alpha1
kind: App
metadata:
  name: restra
spec:
  image: registry.example.com/restra-api:1.14
  port: 8080
  replicas: 2
  domain: restra.example.com
  backup:
    schedule: "0 3 * * *"
    image: rclone/rclone:1.68
    s3:
      endpoint: https://fsn1.your-objectstorage.com
      bucket: albaraa
      prefix: restra/
      secretRef:
        name: s3-credentials
```

`kubectl apply` that, and shipmate reconciles a Deployment, a Service, an
Ingress, and a nightly backup CronJob pointed at S3-compatible object storage.
Edit it, and the cluster follows. Delete it, and everything it created is
garbage-collected.

## Why

I run a fleet of small production apps (.NET and Node) on a single Hetzner
box: one docker-compose file, one Traefik label block, one `.env`, one deploy
script, and one backup cron per app. Every new app means re-deriving the same
five artifacts by hand, and drift between apps is inevitable.

shipmate encodes that operational knowledge once, as a controller. It is the
same model-driven idea behind [Juju](https://juju.is) charms — capture how an
app is operated in software, declare *what* you want, and let an operator own
*how* — applied to my own fleet at weekend scale.

## What the controller does

```
              ┌────────────────────────────────────────────────┐
              │                  App (CRD)                     │
              │  image · port · replicas · domain · env        │
              │  resources · backup{schedule,image,s3}         │
              └───────────────────────┬────────────────────────┘
                                      │ reconcile
              ┌───────────────────────┴────────────────────────┐
              │                                                │
   always     │   Deployment ──── Service                      │
              │                                                │
   only if    │   Ingress          (spec.domain set)           │
   requested  │   CronJob → S3     (spec.backup set)           │
              └────────────────────────────────────────────────┘
```

Reconciliation semantics, which are the actual point of the project:

- **Converge, don't script.** Every child is driven through
  `CreateOrUpdate`: the same code path creates a missing child, corrects a
  drifted one, and applies spec changes. `kubectl delete deployment restra`
  and it is back within seconds, because the controller watches everything it
  owns (`Owns(...)`) rather than polling.
- **Optional children are removed, not orphaned.** Clearing `spec.domain`
  deletes the Ingress; clearing `spec.backup` deletes the CronJob. Toggling a
  capability is a pure spec edit with no manual cleanup — the removal path
  most example operators skip.
- **Ownership is respected.** Before deleting a no-longer-requested child,
  the controller verifies via owner reference that it created the object. A
  same-named Ingress created by someone else survives.
- **Cleanup is delegated to garbage collection.** All children carry a
  controller owner reference, so deleting an App cascades. There is
  deliberately **no finalizer**: the controller holds no external state, and
  backup objects in S3 are intentionally retained after an app is deleted —
  auto-deleting backups on resource deletion is a footgun, not a feature.
- **Status is honest and cheap.** `Available` and `Progressing` conditions
  (standard `metav1.Condition`, with `observedGeneration`) mirror the
  Deployment's readiness, plus `readyReplicas` and the external `url`. Status
  writes are skipped when semantically unchanged, so owned-object events
  don't fan out into useless PUTs and re-reconciles.

```console
$ kubectl get apps
NAME     IMAGE                  READY   URL                        AGE
restra   restra-api:1.14        2       http://restra.example.com  3d
hello    nginxdemos/hello:0.4   2       http://hello.local         5m
```

## Spec reference

| Field | Required | Default | Notes |
|---|---|---|---|
| `image` | yes | — | Container image to deploy |
| `port` | no | `8080` | Container port; Service and Ingress route to it |
| `replicas` | no | `1` | `0` is valid (scale to zero, `Available` reason `ScaledToZero`) |
| `domain` | no | — | Set → Ingress at this host; clear → Ingress removed |
| `env` | no | — | Passed verbatim to the container |
| `resources` | no | — | Standard requests/limits |
| `backup.schedule` | with backup | — | Cron expression |
| `backup.image` / `backup.command` | with backup | — | Any image that can reach S3; connection details arrive as `S3_ENDPOINT`, `S3_BUCKET`, `S3_PREFIX` env vars |
| `backup.s3.secretRef` | with backup | — | Secret holding `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY`; credentials are never inlined in the CR |

## Quickstart

On any cluster (kind, minikube, a real one):

```sh
make install                        # CRD
make deploy IMG=<registry>/shipmate:tag
kubectl apply -f config/samples/shipmate_v1alpha1_app.yaml
```

On [MicroK8s](https://microk8s.io) there is a one-shot demo that builds the
image, sideloads it into containerd (no registry needed), deploys the
controller, and applies a demo app with an Ingress:

```sh
./hack/demo-microk8s.sh
```

On macOS, MicroK8s runs in a [Multipass](https://canonical.com/multipass) VM:
`brew install ubuntu/microk8s/microk8s && microk8s install` first.

## Testing

```sh
make test
```

The suite runs against **envtest** — a real `kube-apiserver` + etcd, not
fakes — and covers the behavior that matters: child creation with defaults,
drift correction, Ingress/CronJob add *and remove* on spec toggles, foreign
same-named objects surviving reconciliation, and status condition
transitions. Controller package coverage: ~82%.

## Design decisions

- **Level-based, not edge-based.** Reconcile never asks "what changed?" —
  it recomputes the entire desired state from the spec every pass. This is
  what makes drift correction and crash recovery free.
- **The Deployment selector is derived only from the App name.** Selectors
  are immutable; deriving them from anything a user can edit (image, domain)
  would wedge the Deployment on the first change.
- **Generic backups over built-in database dumps.** A `pg_dump` baked into
  the operator would cover exactly one app shape. A scheduled
  image+command with S3 wiring covers Postgres, SQLite file copies, and
  anything rclone/restic can reach — matching a heterogeneous real fleet.
- **v1alpha1 and honest about it.** No conversion webhooks, no multi-version
  story yet. The API group is versioned so that adding them later is
  additive, not breaking.

## Roadmap

- `ServiceMonitor` support behind a spec flag, guarded by CRD discovery, so
  the controller does not require the Prometheus Operator to exist.
- Validating admission webhook (reject malformed cron expressions and
  domains at admission time instead of at CronJob creation).
- TLS via cert-manager annotations when `spec.domain` is set.
- A [charm](https://juju.is/docs/sdk) port — the same model, expressed in
  Canonical's operator framework.
