#!/usr/bin/env bash
# Render deploy/whoami.yaml for one instance.
#
#   deploy/render.sh deploy/instances/k8s-k3s/payments-processor.env | kubectl apply -f -
#   IMAGE=ghcr.io/jsabo/spiffe-whoami:abc123 deploy/render.sh <instance.env> ...
#
# An instance file sets NAMESPACE and NAME (the ServiceAccount, and therefore the
# SPIFFE ID /svc/NAMESPACE/NAME), and optionally PEERS, AWS_ROLE_ARN,
# AWS_SECRET_ID, AWS_REGION. IMAGE, AWS_ROLE_ARN, AWS_SECRET_ID and AWS_REGION
# from the environment override the file — that is how CI injects account-specific
# values from repository variables while the checked-in files stay placeholders.
# AWS_* env overrides apply only to instances whose file sets AWS_ROLE_ARN
# (commented or not), so payments/ledger stays "not configured".
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
[ $# -ge 1 ] || { echo "usage: $0 <instance.env>..." >&2; exit 2; }

for inst in "$@"; do
  (
    IMAGE_OVERRIDE="${IMAGE:-}"
    ROLE_OVERRIDE="${AWS_ROLE_ARN:-}" SECRET_OVERRIDE="${AWS_SECRET_ID:-}" REGION_OVERRIDE="${AWS_REGION:-}"
    NAMESPACE= NAME= PEERS= AWS_ROLE_ARN= AWS_SECRET_ID= AWS_REGION=us-east-2 IMAGE=ghcr.io/jsabo/spiffe-whoami:latest
    # shellcheck disable=SC1090
    . "$inst"
    [ -n "$IMAGE_OVERRIDE" ] && IMAGE="$IMAGE_OVERRIDE"
    if grep -q '^#*AWS_ROLE_ARN=' "$inst"; then
      [ -n "$ROLE_OVERRIDE" ] && AWS_ROLE_ARN="$ROLE_OVERRIDE"
      [ -n "$SECRET_OVERRIDE" ] && AWS_SECRET_ID="$SECRET_OVERRIDE"
      [ -n "$REGION_OVERRIDE" ] && AWS_REGION="$REGION_OVERRIDE"
    fi
    : "${NAMESPACE:?$inst: NAMESPACE required}" "${NAME:?$inst: NAME required}"
    sed -e "s|\${NAMESPACE}|${NAMESPACE}|g" -e "s|\${NAME}|${NAME}|g" \
        -e "s|\${IMAGE}|${IMAGE}|g" -e "s|\${PEERS}|${PEERS}|g" \
        -e "s|\${AWS_ROLE_ARN}|${AWS_ROLE_ARN}|g" -e "s|\${AWS_SECRET_ID}|${AWS_SECRET_ID}|g" \
        -e "s|\${AWS_REGION}|${AWS_REGION}|g" \
        "${here}/whoami.yaml"
    echo '---'
  )
done
