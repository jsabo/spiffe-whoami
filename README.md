# spiffe-whoami

A pod with no secret in it that still authenticates to AWS and to its neighbours. This
small Go web app shows the SPIFFE identity Teleport issued to the pod it runs in, then
uses it: it exchanges a 15-minute JWT for an AWS role whose trust policy names that one
identity, and it makes mutual-TLS calls to peers that accept or refuse it by identity.
Run the identical manifest in another namespace and both AWS and the peers say no.

It is deployed to Kubernetes by GitHub Actions through Teleport Machine ID, so the
deployer has no kubeconfig and no network route to the cluster either.

Verified against Teleport 18.11.1 (Enterprise Cloud) on k3s, Talos 1.13 and Amazon EKS
1.35, same manifests on all three.

## What you will see

```
spiffe-whoami                                  k8s-prod · payments/processor-…

WHO I AM
  spiffe://example.teleport.sh/svc/payments/processor
  issued by     bot k8s-prod
  derived from  /svc/{{ workload.kubernetes.namespace }}/{{ workload.kubernetes.service_account }}

WHAT THIS POD SAYS ABOUT ITSELF   namespace payments · service account processor · pod processor-7c5b…
X.509 SVID    valid 1h, expires in 58m 33s, renewed in the background
JWT SVID      aud sts.amazonaws.com · ttl 15m · claims: sub, iss, exp, kube.{cluster,namespace,pod}

USING IT: AWS
  sts:GetCallerIdentity  ✓ arn:aws:sts::123456789012:assumed-role/spiffe-payments-processor/svc-payments-processor
  secretsmanager         ✓ demo/payments/processor = pa***…it
  how: the JWT above went to AssumeRoleWithWebIdentity; the role trusts this SPIFFE ID and nothing else.

USING IT: PEERS OVER mTLS
  ledger.payments:8443       ✓ accepted · peer spiffe://…/svc/payments/ledger
  processor.analytics:8443   ✗ refused by peer · remote error: tls: bad certificate
```

And the same page for `analytics/processor`: ID `/svc/analytics/processor`, AWS `403`,
both payments peers `refused`. Nobody configured either identity. The namespace and
ServiceAccount were attested by the kubelet; Teleport rendered them into the ID.

Terms, if any are new: an **SVID** is a SPIFFE Verifiable Identity Document, the identity
as a credential (an X.509 certificate or a JWT). **mTLS** is TLS where both sides present
a certificate. **OIDC federation** is AWS accepting a JWT from an issuer it trusts in
exchange for role credentials. The **Workload API** is the socket a pod asks for its
identity. Ten-minute primer:
[teleport-workload-identity-k8s/docs/concepts.md](https://github.com/jsabo/teleport-workload-identity-k8s/blob/main/docs/concepts.md).

## Before you start

- The issuer from [teleport-workload-identity-k8s](https://github.com/jsabo/teleport-workload-identity-k8s)
  installed on the cluster (`scripts/check.sh` there reports healthy). This app only
  consumes the socket that issuer's CSI driver provides.
- `tsh` and `kubectl` logged in to that cluster with rights to create namespaces,
  ServiceAccounts, Deployments and Services.
- Optional, for the AWS section: an AWS account where you can create an IAM OIDC
  provider, a role and a Secrets Manager secret; Terraform 1.5+.
- Optional, for the CI chapter: a GitHub repository you own.

## Quick start

Two namespaces, three instances, one page. Replace `my-cluster` with your Teleport
Kubernetes cluster name; nothing else needs editing.

```bash
tsh kube login my-cluster
kubectl create namespace payments analytics

IMAGE=ghcr.io/jsabo/spiffe-whoami:latest \
  deploy/render.sh deploy/instances/example/*.env | kubectl apply -f -
kubectl -n payments rollout status deploy/processor      # Ready means an SVID was issued

kubectl -n payments port-forward svc/processor 8080 &
open http://localhost:8080                                # or curl -s localhost:8080/whoami.json | jq
```

The readiness probe fails until the pod holds an X.509 SVID, so a completed rollout is
itself proof that the identity was issued.

An instance file (`deploy/instances/example/*.env`) sets the namespace and the
ServiceAccount, which together are the identity, plus the peers to call and, optionally,
the AWS role to try. The three examples are the demo: `payments/processor`,
`payments/ledger`, `analytics/processor`. The `k8s-k3s`, `k8s-talos` and `k8s-eks`
directories are the author's clusters, deployed by CI; they are identical apart from the
real AWS role ARN.

## Walkthrough

### 1. Read the identity

Open the page for `payments/processor` (the block under "What you will see"). The ID was
not configured anywhere in this repository or in the pod. One templated
`workload_identity` on the Teleport side, plus the namespace and ServiceAccount the
kubelet attested, produced it. The line "what this pod says about itself" comes from the
Kubernetes Downward API, so you can see the attested facts and the pod's own view agree.

### 2. Use it against AWS

`aws/main.tf` creates an OIDC identity provider for your Teleport cluster, one role whose
trust policy allows exactly one `sub`, and one secret that role may read:

```bash
cd aws && terraform init && terraform apply \
  -var proxy_host=example.teleport.sh \
  -var thumbprint="$(curl -s https://example.teleport.sh/webapi/thumbprint | tr -d '"')" \
  -var spiffe_id=spiffe://example.teleport.sh/svc/payments/processor
cd ..
```

Copy `role_arn` from the output into `deploy/instances/example/payments-processor.env`
(uncomment the three `AWS_` lines), re-run the render and apply from the quick start, and
reload the page:

```
sts:GetCallerIdentity  ✓ arn:aws:sts::123456789012:assumed-role/spiffe-payments-processor/svc-payments-processor
secretsmanager         ✓ demo/payments/processor = pa***…it
```

The app fetched a JWT SVID for audience `sts.amazonaws.com` from the Workload API and
handed it to `AssumeRoleWithWebIdentity`. No access key exists on the pod, in the cluster,
or in this repository. Now uncomment the same three lines in
`analytics-processor.env`, apply, and open that page: same role ARN, `403`, because its
`sub` is `/svc/analytics/processor`. The role's trust policy is the whole authorization.

### 3. Use it against peers

Every instance serves `/whoami.json` on a mutual-TLS listener and accepts only clients
whose SPIFFE ID is under `/svc/<its own namespace>/`. On the `payments/processor` page:

```
ledger.payments:8443       ✓ accepted  · peer spiffe://…/svc/payments/ledger
processor.analytics:8443   ✗ refused by peer · remote error: tls: bad certificate
```

The refusal happened at the TLS handshake on the analytics side; no request reached a
handler. The policy is a prefix check on the SPIFFE ID, which is what the ID structure
(`/svc/<project>/<service>`) was designed for.

### What to expect

| Measured, Teleport 18.11.1 | |
|---|---|
| Page render, all sections, first load | 1.3 s, dominated by the STS exchange |
| Page render with the AWS result cached (30 s) | under 100 ms |
| JWT SVID fetch from the Workload API | under 50 ms |
| Same-project mTLS call | 230 ms first call |
| Cross-project refusal | 6 ms, at the handshake |
| CI: multi-arch image build + deploy to three clusters | 100 s (77 s build with cache, 23 s per cluster in parallel) |

## How it works

```
 pod ── Workload API (csi volume) ──► tbot on the node ──attested facts──► Teleport Auth ──SVIDs──► pod
  │
  ├── X.509 SVID ──mTLS──► peer pod: accept if spiffe://<td>/svc/<my namespace>/*
  └── JWT SVID (aud sts.amazonaws.com) ──AssumeRoleWithWebIdentity──► AWS role trusting one sub

 GitHub Actions ──GitHub OIDC token──► Teleport ──1h cert──► kubectl ──► proxy ──► kube agent ──► cluster
```

- `identity.go`: go-spiffe `X509Source` and `JWTSource` on the socket. The SVID and trust
  bundle stay current in memory; there are no files.
- `aws.go`: the AWS SDK's `WebIdentityRoleProvider` with an in-memory token retriever that
  fetches a fresh JWT SVID whenever STS asks. The result is cached for 30 s so the page
  can be refreshed during a demo; the page says when it is showing a cached result.
- `peers.go`: `tlsconfig.MTLSServerConfig` with an authorizer that accepts the trust domain
  plus the `/svc/<own namespace>/` prefix; the client side accepts any member of the trust
  domain and reports who answered.
- `web.go`, `index.html`: the report as a page and as JSON. `/whoami.json` on the mTLS port
  skips the peer fan-out so two instances do not call each other forever.
- `deploy/whoami.yaml`: one Deployment, Service and ServiceAccount per instance, the
  socket as a `csi.spiffe.io` volume (allowed under Pod Security `baseline`), no secrets.

## 5-minute demo script

1. `kubectl -n payments get deploy processor -o yaml | grep -ci secret` → "Zero. Nothing in
   this pod is a credential."
2. Open the `payments/processor` page → "Its identity was computed from namespace and
   ServiceAccount; nothing in this repo names it."
3. `tctl get workload_identity/svc` → "One template on the Teleport side. Every cluster
   shares it."
4. Point at the green AWS row → "A 15-minute JWT, exchanged with STS. The IAM trust policy
   names one SPIFFE ID."
5. Open the `analytics/processor` page → "Same manifest, different namespace: AWS says 403,
   the payments peers refuse at the TLS handshake."
6. `tctl lock --user=bot-k8s-prod --ttl=5m` → "Kill switch: this cluster's issuer stops
   within a renewal."
7. Open the GitHub Actions run → "The thing that deployed all of this had no kubeconfig
   either."

## Deploying with GitHub Actions and Machine ID

This chapter is a complete demo of Teleport Machine ID on its own. The workflow in
`.github/workflows/deploy.yml` builds the image, then in a second job with
`id-token: write`:

1. `teleport-actions/setup` installs `tbot` at the version your cluster advertises.
2. `teleport-actions/auth-k8s` runs `tbot` with GitHub's OIDC token for the job. Teleport
   verifies that token against GitHub's public keys and checks it against the join
   token's rules: this repository, this branch. No secret is stored in GitHub.
3. Teleport issues the bot a one-hour certificate and a kubeconfig that routes through the
   Teleport proxy to the cluster's own agent. The runner never learns a cluster address.
4. `deploy/render.sh` and `kubectl apply` for each instance file; the Teleport role
   limits the bot to two namespaces, and namespaced RoleBindings (`deploy/rbac.yaml`) cap
   it at the built-in `edit` role there.

Four edits before your first push:

| File | Change |
|---|---|
| `teleport/bot-token-deploy.yaml` | `repository: YOUR-GITHUB-USER/spiffe-whoami` → your fork |
| `teleport/role-deploy.yaml` | `kubernetes_labels` → a label your clusters carry |
| `.github/workflows/deploy.yml` | `TELEPORT_PROXY` → your proxy; `matrix.cluster` → your Teleport Kubernetes cluster names, one `deploy/instances/<name>/` directory each |
| `deploy/instances/<name>/*.env` | copy from `example/`; set `AWS_ROLE_ARN` if you applied `aws/` |

Teleport side, once:

```bash
kubectl create namespace payments analytics      # on each cluster
kubectl apply -f deploy/rbac.yaml                 # on each cluster, as an admin
tctl create -f teleport/role-deploy.yaml
tctl create -f teleport/bot-token-deploy.yaml
tctl bots add spiffe-whoami-deploy --roles=spiffe-whoami-deploy --token=spiffe-whoami-deploy
```

Push to `main`. What you should see in the run log: `Fetched new bot identity ...
spiffe-whoami-deploy`, three `kubectl apply` outputs per cluster, three `rollout status`
successes, and in the Teleport audit log a `kube.request` row per call attributed to
`bot-spiffe-whoami-deploy`. Ask the bot for `kubectl get pods -A` and it gets a 403, which
is also in the audit log: the role covers two namespaces and nothing else.
`tctl bots instances ls` shows the bot only while a job runs; the instance record expires
with the certificate.

## Day two

- **Rotation**: nothing to do. X.509 SVIDs renew at half life inside the process; JWTs
  are fetched per use; role credentials last as long as STS grants.
- **Adding an instance**: one `.env` file. Adding a project: a namespace, a RoleBinding in
  `deploy/rbac.yaml`, and the namespace in `teleport/role-deploy.yaml`.
- **Adding a cluster**: an instance directory, the cluster in the workflow matrix, and an
  issuer on that cluster.
- **Trusting a second service in AWS**: a second role with its own `sub` condition; the
  OIDC provider is shared.
- **Revoking**: `tctl lock --user=bot-spiffe-whoami-deploy` stops deploys immediately;
  `tctl lock --user=bot-<cluster>` stops issuance on a cluster within one renewal.

## Layout

```
main.go identity.go aws.go peers.go web.go index.html   the app (single package); peers_test.go
Dockerfile                                              multi-arch, distroless, nonroot
deploy/whoami.yaml                                      the instance template
deploy/render.sh                                        fill an instance .env into the template
deploy/instances/example/*.env                          start here; placeholders only
deploy/instances/{k8s-k3s,k8s-talos,k8s-eks}/*.env      the author's clusters, deployed by CI
deploy/rbac.yaml                                        namespaced RoleBindings for the CI bot
teleport/role-deploy.yaml  teleport/bot-token-deploy.yaml   the CI bot's role and github join token
aws/main.tf                                             OIDC provider, role, demo secret
.github/workflows/deploy.yml                            test, build, then deploy through Teleport
```

## License

Apache-2.0.
