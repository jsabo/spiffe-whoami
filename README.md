# spiffe-whoami

A small Go web app that shows the SPIFFE identity Teleport issued to the pod it runs in,
then uses it. One page: the SPIFFE ID and the facts it was derived from, the X.509 SVID
with its lifetime, the JWT SVID decoded, a call to AWS that succeeds because an IAM role
trusts that SPIFFE ID and nothing else, and mutual-TLS calls to its neighbours that are
accepted or refused by their SPIFFE ID. There is no secret in the container, its
manifest, or the repository.

It is deployed to Kubernetes by GitHub Actions through Teleport Machine ID: the runner
holds no kubeconfig and has no network route to the cluster.

Verified against Teleport 18.11.1 (Enterprise Cloud) on k3s: identities issued in under
a second, AWS role assumed with a 15-minute JWT in 1.3 s, a same-project peer accepted in
230 ms, a cross-project peer refused at the TLS handshake in 6 ms, and a push to `main`
built the image for two architectures and rolled it out in 100 s.

## Start here

1. **Understand** the six words this depends on: trust domain, SPIFFE ID, SVID, trust
   bundle, Workload API, attestation. Ten minutes:
   [teleport-workload-identity-k8s/docs/concepts.md](https://github.com/jsabo/teleport-workload-identity-k8s/blob/main/docs/concepts.md).
2. **Install the issuer** on your cluster:
   [teleport-workload-identity-k8s](https://github.com/jsabo/teleport-workload-identity-k8s).
   This app only consumes what that DaemonSet provides.
3. **Deploy the app by hand once** (quick start), read the page, then let CI do it.

## Quick start

Your values: the proxy address, the cluster name, and an AWS account if you want the
AWS section to light up. The issuer from step 2 must already be running.

```bash
tsh login --proxy=example.teleport.sh:443
tsh kube login my-cluster

kubectl apply -f deploy/rbac.yaml                       # RoleBindings for the CI bot, admin, once
IMAGE=ghcr.io/jsabo/spiffe-whoami:latest \
  deploy/render.sh deploy/instances/k8s-k3s/*.env | kubectl apply -f -
kubectl -n payments rollout status deploy/processor

kubectl -n payments port-forward svc/processor 8080     # then open http://localhost:8080
```

Instance files (`deploy/instances/<cluster>/*.env`) set the namespace and ServiceAccount,
which is all the identity depends on, plus the peers to call and the AWS role to try.
The three shipped instances are the demo: `payments/processor`, `payments/ledger`, and
`analytics/processor`.

### Phase by phase

**Read the identity.** Open the page for `payments/processor`. What you should see:

```
spiffe://example.teleport.sh/svc/payments/processor
issued by bot k8s-k3s
namespace payments · service account processor · pod processor-…    (Downward API, matches the SVID)
X.509 SVID   valid 1h, expires in 58m, renewed in the background
JWT SVID     aud sts.amazonaws.com, ttl 15m, claims: sub, iss, kube.cluster/namespace/pod
```

The ID was not configured anywhere in this repo. It came from one templated resource on
the Teleport side and the namespace and ServiceAccount the kubelet attested.

**Use it against AWS.** `aws/main.tf` creates an OIDC identity provider for your Teleport
cluster, a role whose trust policy allows exactly one `sub`, and one secret:

```bash
cd aws && terraform init && terraform apply \
  -var proxy_host=example.teleport.sh \
  -var thumbprint="$(curl -s https://example.teleport.sh/webapi/thumbprint | tr -d '"')" \
  -var spiffe_id=spiffe://example.teleport.sh/svc/payments/processor
```

Reload the page. What you should see:

```
sts:GetCallerIdentity  ✓ arn:aws:sts::123456789012:assumed-role/spiffe-payments-processor/svc-payments-processor
secretsmanager         ✓ demo/payments/processor = pa***…it
```

The app fetched a JWT SVID for audience `sts.amazonaws.com` from the Workload API and
handed it to `AssumeRoleWithWebIdentity`. No access key exists on the pod. Open the
`analytics/processor` page: same manifest, same role ARN, and AWS answers 403, because
its `sub` is `/svc/analytics/processor`.

**Use it against peers.** Every instance serves `/whoami.json` on an mTLS listener and
accepts only clients whose SPIFFE ID is under `/svc/<its own namespace>/`. What you
should see on `payments/processor`:

```
ledger.payments:8443       ✓ accepted  · peer spiffe://…/svc/payments/ledger
processor.analytics:8443   ✗ refused by peer · remote error: tls: bad certificate
```

The refusal happened at the TLS handshake on the analytics side; no request reached a
handler. The policy is a prefix check on the SPIFFE ID, which is what the ID structure
was designed for.

### What to expect

| Measured on k3s, Teleport 18.11.1 | |
|---|---|
| Page render, all sections | 1.3 s, dominated by the STS exchange |
| JWT SVID fetch | under 50 ms |
| AWS `AssumeRoleWithWebIdentity` + `GetCallerIdentity` | 1.2 s |
| Same-project mTLS call | 230 ms first call |
| Cross-project refusal | 6 ms, at the handshake |
| CI: multi-arch image build + deploy of three instances | 100 s (77 s build with cache, 23 s deploy) |

### What you need

- The issuer DaemonSet from `teleport-workload-identity-k8s` running on the cluster.
- A namespace per project and a ServiceAccount per instance; `deploy/render.sh` creates the
  ServiceAccounts, you create the namespaces.
- For AWS: an account where you can create an IAM OIDC provider, a role and a Secrets
  Manager secret; Terraform 1.5+.
- For CI: a GitHub repository you own, and on the Teleport side a bot with the `github`
  join method (below).

## Deploying with GitHub Actions and Machine ID

This is a complete demo of Teleport Machine ID on its own. The workflow in
`.github/workflows/deploy.yml` builds the image and then, in a second job with
`id-token: write`, does this:

1. `teleport-actions/setup` installs `tbot` at the version your cluster advertises.
2. `teleport-actions/auth-k8s` runs `tbot` with GitHub's OIDC token for the job. Teleport
   verifies that token against GitHub's public keys and checks it against the join token's
   rules: this repository, this branch. No secret is stored in GitHub.
3. Teleport issues the bot a one-hour certificate and a kubeconfig that routes through the
   Teleport proxy to the cluster's own agent. The runner never learns the cluster's address.
4. `kubectl apply` renders and applies each instance file; the role limits the bot to two
   namespaces, and namespaced RoleBindings (`deploy/rbac.yaml`) cap it at `edit` there.

Teleport side, once:

```bash
tctl create -f teleport/role-deploy.yaml
tctl create -f teleport/bot-token-deploy.yaml         # edit the repository name first
tctl bots add spiffe-whoami-deploy --roles=spiffe-whoami-deploy --token=spiffe-whoami-deploy
```

Then set `TELEPORT_PROXY` in the workflow to your proxy and push to `main`. What you
should see in the run log: `Fetched new bot identity ... spiffe-whoami-deploy`, the
three `kubectl apply` outputs, three `rollout status` successes, and in the Teleport audit
log a `kube.request` row per call attributed to `bot-spiffe-whoami-deploy`. Ask the bot
for `kubectl get pods -A` and it gets a 403, which is also in the audit log: the role
covers two namespaces and nothing else.

`tctl bots instances ls` shows the bot only while a job runs; the instance record expires
with the certificate.

## How it works

```
 GitHub Actions ──OIDC token──► Teleport ──1h cert──► kubectl ──► proxy ──► kube agent ──► cluster
                                                                              (namespaces payments, analytics)

 pod ── Workload API socket ──► tbot (node) ──attested facts──► Teleport Auth ──SVIDs──► pod
  │
  ├── X.509 SVID ──mTLS──► peer pod: accept if spiffe://td/svc/<my namespace>/*
  └── JWT SVID (aud sts.amazonaws.com) ──AssumeRoleWithWebIdentity──► AWS role trusting one sub
```

- `identity.go`: go-spiffe `X509Source` and `JWTSource` on the socket; the SVID and
  bundle stay current in memory.
- `aws.go`: the AWS SDK's `WebIdentityRoleProvider` with an in-memory token retriever
  that fetches a fresh JWT SVID each time STS asks. The token never touches disk.
- `peers.go`: `tlsconfig.MTLSServerConfig` with an authorizer that accepts the trust
  domain and the `/svc/<own namespace>/` prefix; the client side accepts any member of
  the trust domain and reports who answered.
- `web.go` and `index.html`: the report as a page and as JSON. `/whoami.json` on the
  mTLS port skips the peer fan-out so two instances do not call each other forever.

## Day two

- **Rotation**: nothing to do. X.509 SVIDs renew at half life inside the process; JWTs
  are fetched per use; the AWS role credentials last as long as STS grants.
- **Adding an instance**: one `.env` file. Adding a project: a namespace, a RoleBinding
  in `deploy/rbac.yaml`, and the namespace in `teleport/role-deploy.yaml`.
- **Adding a cluster**: a directory under `deploy/instances/`, the cluster in the
  workflow matrix, and an issuer on that cluster.
- **Trusting a second service in AWS**: a second role with its own `sub` condition. The
  OIDC provider is shared.
- **Revoking**: `tctl lock --user=bot-spiffe-whoami-deploy` stops deploys immediately;
  `tctl lock --user=bot-<cluster>` stops issuance on a cluster within one renewal.

## Layout

```
main.go identity.go aws.go peers.go web.go index.html   the app (single package)
Dockerfile                                              multi-arch, distroless, nonroot
deploy/whoami.yaml                                      the instance template
deploy/render.sh                                        fill an instance .env into the template
deploy/instances/<cluster>/*.env                        one file per running instance
deploy/rbac.yaml                                        namespaced RoleBindings for the CI bot
teleport/role-deploy.yaml  teleport/bot-token-deploy.yaml   the CI bot's role and github join token
aws/main.tf                                             OIDC provider, role, demo secret
.github/workflows/deploy.yml                            build, then deploy through Teleport
```

## License

Apache-2.0.
