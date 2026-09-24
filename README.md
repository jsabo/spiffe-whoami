# spiffe-whoami

A small web app that shows the SPIFFE identity Teleport issued to the pod it runs in,
then uses it: it assumes an AWS role with no access key, and it makes mutual-TLS calls
that peer pods accept or refuse by identity alone. It is deployed by GitHub Actions
through Teleport Machine ID, so the deployer holds no kubeconfig either.

Verified on k3s, Talos and Amazon EKS against Teleport 18.11.1 (Enterprise), same
manifests on all three.

## What it shows

The page for the instance running as ServiceAccount `processor` in namespace `payments`:

```
WHO I AM        spiffe://example.teleport.sh/svc/payments/processor      issued by bot k8s-prod
                derived from /svc/{{ workload.kubernetes.namespace }}/{{ workload.kubernetes.service_account }}

SVIDs           X.509  valid 1h, renewed in the background
                JWT    aud sts.amazonaws.com · 15m · claims sub, iss, exp, kube.{cluster,namespace,pod}

AWS             sts:GetCallerIdentity  ✓ arn:aws:sts::123456789012:assumed-role/spiffe-payments-processor/…
                secretsmanager         ✓ demo/payments/processor = pa***…it

PEERS (mTLS)    ledger.payments:8443       accepted · peer spiffe://…/svc/payments/ledger
                processor.analytics:8443   refused, as intended · analytics accepts only /svc/analytics/*
                                           (decided at the TLS handshake; alert: bad certificate)
```

The same page for `analytics/processor` shows a different ID, AWS `403`, and both
payments peers refusing. Three things carry the value:

- **The pod holds no credential.** No access key, no client certificate, no token in the
  manifest or the image. The identity was computed by Teleport from the namespace and
  ServiceAccount the kubelet attested.
- **The same manifest in another namespace is refused everywhere.** AWS refuses because the
  role's trust policy names one SPIFFE ID. The peers refuse at the TLS handshake because
  they accept only their own project's prefix.
- **The pipeline that deployed it had no secret either.** GitHub Actions joined Teleport
  with GitHub's own OIDC token and received a one-hour certificate scoped to two namespaces.

Terms, defined once. **SPIFFE** (Secure Production Identity Framework For Everyone) is the
open standard for workload identity. An **SVID** (SPIFFE Verifiable Identity Document) is
the identity as a credential: an X.509 certificate or a JWT. **mTLS** is TLS where both
sides present a certificate. **OIDC federation** is AWS accepting a JWT from an issuer it
trusts in exchange for role credentials. The **Workload API** is the Unix socket a pod asks
for its SVIDs; the **trust domain** is the issuer's name, here your Teleport cluster.

## The components

| Component | Where it runs | What it does | File |
|---|---|---|---|
| Issuer | every node | Teleport Workload Identity: a `tbot` with the `workload-identity-api` service on each node, attesting pods through the kubelet, with the SPIFFE CSI driver delivering its socket into pods as a `csi.spiffe.io` volume. Any SPIFFE Workload API served the same way works. Install it first. | not in this repo |
| spiffe-whoami | a pod per instance, in `payments` and `analytics` | Reads its SVIDs from the socket with go-spiffe, serves the page and `/whoami.json`, calls AWS and its peers. Distroless, non-root, no files. | `main.go`, `identity.go`, `aws.go`, `peers.go`, `web.go`, `index.html` |
| Instance files | your machine or CI | One `.env` per instance: the namespace and ServiceAccount (which together are the identity), the peers to call, and optionally the AWS role. `render.sh` fills them into the one manifest. | `deploy/instances/example/*.env`, `deploy/whoami.yaml`, `deploy/render.sh` |
| AWS side | your AWS account | An OIDC identity provider for your Teleport cluster, one IAM role whose trust policy allows exactly one SPIFFE ID as `sub`, and one Secrets Manager secret that role may read. | `aws/main.tf` |
| Deploy bot | Teleport and GitHub Actions | Bot `spiffe-whoami-deploy` joins with GitHub's OIDC token (join method `github`, pinned to your repository and branch). Its Teleport role reaches two namespaces on labelled clusters; Kubernetes RoleBindings cap it at `edit` there. | `.github/workflows/deploy.yml`, `teleport/role-deploy.yaml`, `teleport/bot-token-deploy.yaml`, `deploy/rbac.yaml` |

## How it works

### Using the identity

```
 pod ── Workload API (csi volume) ──► issuer on the node ──► Teleport Auth ──► X.509 SVID + JWT SVID
  │
  ├── JWT (aud sts.amazonaws.com) ──AssumeRoleWithWebIdentity──► AWS role trusting one sub ──► temporary credentials
  └── X.509 ──mTLS──► peer pod, which accepts only spiffe://<trust domain>/svc/<its own namespace>/*
```

1. On start the app opens the Workload API socket and keeps an X.509 SVID and the trust
   bundle current in memory. The readiness probe fails until it has one, so a completed
   rollout is proof that an identity was issued.
2. For AWS it fetches a JWT SVID for audience `sts.amazonaws.com` and hands it to
   `AssumeRoleWithWebIdentity`. AWS verifies the signature against Teleport's published
   keys and checks the trust policy's `sub` condition. The result is cached for 30 seconds
   so the page can be refreshed during a demo.
3. For peers it serves `/whoami.json` on a second port with mTLS. The server accepts a
   client only if its SPIFFE ID is under `/svc/<own namespace>/`. A cross-project caller
   is refused during the handshake; no request reaches a handler.

### Deploying it

```
 GitHub Actions ──GitHub OIDC token──► Teleport ──1h cert + kubeconfig──► kubectl ──► Teleport proxy ──► cluster agent
```

1. `teleport-actions/setup` installs `tbot` at the version your cluster advertises.
2. `teleport-actions/auth-k8s` joins Teleport with the job's OIDC token. Teleport checks it
   against GitHub's public keys and the join token's rules: this repository, this branch.
3. Teleport issues a one-hour certificate and a kubeconfig that routes through the Teleport
   proxy to the cluster's own agent. The runner never learns a cluster address.
4. The job renders and applies each instance file. Every `kubectl` call is a `kube.request`
   audit event attributed to `bot-spiffe-whoami-deploy`; a cluster-wide `get pods -A` is a
   403, because the role covers two namespaces and nothing else.

## Install

You need a Teleport Workload Identity issuer on the cluster that delivers the Workload API
as a `csi.spiffe.io` volume (any pod declaring that volume gets an SVID), and
`kubectl` logged in through Teleport with rights to create namespaces and Deployments.
Replace `my-cluster` with your Teleport Kubernetes cluster name.

```bash
tsh kube login my-cluster
kubectl create namespace payments analytics

IMAGE=ghcr.io/jsabo/spiffe-whoami:latest \
  deploy/render.sh deploy/instances/example/*.env | kubectl apply -f -
kubectl -n payments rollout status deploy/processor

kubectl -n payments port-forward svc/processor 8080 &
open http://localhost:8080          # or: curl -s localhost:8080/whoami.json | jq
```

The AWS rows read "not configured" until the next section. The peer rows already show
`ledger` accepting and `analytics` refusing.

## Try it

### AWS without a key

```bash
cd aws && terraform init && terraform apply \
  -var proxy_host=example.teleport.sh \
  -var thumbprint="$(curl -s https://example.teleport.sh/webapi/thumbprint | tr -d '"')" \
  -var spiffe_id=spiffe://example.teleport.sh/svc/payments/processor
cd ..
```

Uncomment the three `AWS_` lines in `deploy/instances/example/payments-processor.env`,
put the `role_arn` output in, re-run the render and apply, and reload the page. The AWS
rows turn green. Then do the same in `analytics-processor.env`: same role ARN, `403`,
because that pod's `sub` is `/svc/analytics/processor`. The trust policy is the whole
authorization.

### Peers over mTLS

Already visible on the `payments/processor` page. `ledger.payments` shares the project and
is accepted; `processor.analytics` is refused with `tls: bad certificate`. Open the
`analytics/processor` page and both payments peers refuse it in turn.

First page load takes about 1.3 seconds, almost all of it the STS exchange. A refresh
within 30 seconds renders in under 100 milliseconds.

## Deploy with GitHub Actions

Nothing in the workflow names a tenant, a cluster or an AWS account. Those are repository
variables, so a fork needs two file edits and no more.

| Repository variable | Value |
|---|---|
| `TELEPORT_PROXY` | `example.teleport.sh:443` |
| `KUBE_CLUSTERS` | JSON list of Teleport Kubernetes cluster names, e.g. `["k8s-prod"]`. One deploy job per entry |
| `AWS_ROLE_ARN`, `AWS_SECRET_ID`, `AWS_REGION` | optional, the outputs of `aws/main.tf`. Applied to the two `processor` instances, never to `ledger` |

```bash
gh variable set TELEPORT_PROXY --body example.teleport.sh:443
gh variable set KUBE_CLUSTERS  --body '["k8s-prod"]'
```

Edit `teleport/bot-token-deploy.yaml` (`repository:` to your fork) and
`teleport/role-deploy.yaml` (`kubernetes_labels` to a label your clusters carry). Then,
once:

```bash
kubectl apply -f deploy/rbac.yaml                 # on each cluster, as an admin
tctl create -f teleport/role-deploy.yaml
tctl create -f teleport/bot-token-deploy.yaml
tctl bots add spiffe-whoami-deploy --roles=spiffe-whoami-deploy --token=spiffe-whoami-deploy
```

Push to `main`. The run log shows `Fetched new bot identity ... spiffe-whoami-deploy`,
three applies and three successful rollouts per cluster. `tctl bots instances ls` lists
one instance per running job; each record expires with its certificate.

## 5-minute demo script

1. `kubectl -n payments get deploy processor -o yaml | grep -ci secret` → "Zero. Nothing in
   this pod is a credential."
2. Open the `payments/processor` page → "Its identity was computed from namespace and
   ServiceAccount. Nothing in this repo names it."
3. `tctl get workload_identity/svc` → "One template on the Teleport side. Every cluster
   shares it."
4. The green AWS rows → "A 15-minute JWT, exchanged with STS. The IAM trust policy names
   one SPIFFE ID."
5. Open the `analytics/processor` page → "Same manifest, different namespace. AWS says 403
   and the payments peers refuse at the handshake."
6. `tctl lock --user=bot-k8s-prod --ttl=5m` → "Kill switch: this cluster's issuer stops
   within a renewal."
7. Open the GitHub Actions run → "The thing that deployed all of this had no kubeconfig
   either."

## Day two

- **Rotate**: nothing to do. X.509 SVIDs renew inside the process, JWTs are fetched per
  use, and AWS credentials last as long as STS grants.
- **Add an instance**: one `.env` file. Add a project: a namespace, a RoleBinding in
  `deploy/rbac.yaml`, and the namespace in `teleport/role-deploy.yaml`.
- **Add a cluster**: run an issuer on it and add its name to `KUBE_CLUSTERS`.
- **Trust a second service in AWS**: a second role with its own `sub` condition. The OIDC
  provider is shared.
- **Revoke**: `tctl lock --user=bot-spiffe-whoami-deploy` stops deploys at once;
  `tctl lock --user=bot-<cluster>` stops issuance on that cluster within one renewal.

## License

Apache-2.0.
