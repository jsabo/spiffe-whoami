#!/usr/bin/env bash
# Render deploy/whoami.yaml for one instance.
#
#   deploy/render.sh deploy/instances/k8s-k3s/payments-processor.env | kubectl apply -f -
#   IMAGE=ghcr.io/jsabo/spiffe-whoami:abc123 deploy/render.sh <instance.env> ...
#
# An instance file sets NAMESPACE and NAME (the ServiceAccount, and therefore the
# SPIFFE ID /svc/NAMESPACE/NAME), and optionally PEERS, AWS_ROLE_ARN,
# AWS_SECRET_ID, AWS_REGION. IMAGE from the environment overrides the file.
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
[ $# -ge 1 ] || { echo "usage: $0 <instance.env>..." >&2; exit 2; }

for inst in "$@"; do
  (
    IMAGE_OVERRIDE="${IMAGE:-}"
    NAMESPACE= NAME= PEERS= AWS_ROLE_ARN= AWS_SECRET_ID= AWS_REGION=us-east-2 IMAGE=ghcr.io/jsabo/spiffe-whoami:latest
    # shellcheck disable=SC1090
    . "$inst"
    [ -n "$IMAGE_OVERRIDE" ] && IMAGE="$IMAGE_OVERRIDE"
    : "${NAMESPACE:?$inst: NAMESPACE required}" "${NAME:?$inst: NAME required}"
    sed -e "s|\${NAMESPACE}|${NAMESPACE}|g" -e "s|\${NAME}|${NAME}|g" \
        -e "s|\${IMAGE}|${IMAGE}|g" -e "s|\${PEERS}|${PEERS}|g" \
        -e "s|\${AWS_ROLE_ARN}|${AWS_ROLE_ARN}|g" -e "s|\${AWS_SECRET_ID}|${AWS_SECRET_ID}|g" \
        -e "s|\${AWS_REGION}|${AWS_REGION}|g" \
        "${here}/whoami.yaml"
    echo '---'
  )
done
