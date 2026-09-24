# AWS side of the demo: trust the Teleport cluster as an OIDC identity provider,
# let exactly one SPIFFE ID assume a role, and give that role one secret to read.
#
# Nothing here is a credential. The workload proves who it is with a 15-minute
# JWT SVID whose `sub` is its SPIFFE ID; STS checks the signature against the
# Teleport cluster's JWKS and the claims against the trust policy below.
#
#   cd aws && terraform init && terraform apply \
#     -var proxy_host=example.teleport.sh \
#     -var spiffe_id=spiffe://example.teleport.sh/svc/payments/processor
#
# thumbprint: curl -s https://<proxy_host>/webapi/thumbprint

terraform {
  required_version = ">= 1.5"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
}

provider "aws" {
  region = var.region
}

variable "region" {
  type    = string
  default = "us-east-2"
}

variable "proxy_host" {
  description = "Public hostname of the Teleport Proxy Service (no scheme, no port)"
  type        = string
}

variable "thumbprint" {
  description = "SHA-1 thumbprint of the proxy's TLS certificate: curl -s https://<proxy_host>/webapi/thumbprint"
  type        = string
}

variable "spiffe_id" {
  description = "The one SPIFFE ID allowed to assume the role"
  type        = string
}

variable "role_name" {
  type    = string
  default = "spiffe-payments-processor"
}

variable "secret_name" {
  type    = string
  default = "demo/payments/processor"
}

locals {
  issuer = "${var.proxy_host}/workload-identity"
}

# The Teleport cluster as an identity provider. One provider serves every
# workload identity the cluster issues; roles decide who may use it.
resource "aws_iam_openid_connect_provider" "teleport" {
  url             = "https://${local.issuer}"
  client_id_list  = ["sts.amazonaws.com"]
  thumbprint_list = [var.thumbprint]
}

# The role names the service, not the cluster or the pod: the SPIFFE ID is the
# same wherever payments/processor runs.
resource "aws_iam_role" "workload" {
  name = var.role_name
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Federated = aws_iam_openid_connect_provider.teleport.arn }
      Action    = "sts:AssumeRoleWithWebIdentity"
      Condition = {
        StringEquals = {
          "${local.issuer}:aud" = "sts.amazonaws.com"
          "${local.issuer}:sub" = var.spiffe_id
        }
      }
    }]
  })
}

resource "aws_secretsmanager_secret" "demo" {
  name                    = var.secret_name
  recovery_window_in_days = 0
}

resource "aws_secretsmanager_secret_version" "demo" {
  secret_id     = aws_secretsmanager_secret.demo.id
  secret_string = "payments-db-password-rotated-by-nobody-because-no-workload-holds-it"
}

resource "aws_iam_role_policy" "read_secret" {
  name = "read-demo-secret"
  role = aws_iam_role.workload.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect   = "Allow"
      Action   = ["secretsmanager:GetSecretValue"]
      Resource = aws_secretsmanager_secret.demo.arn
    }]
  })
}

output "role_arn" {
  value = aws_iam_role.workload.arn
}

output "secret_id" {
  value = aws_secretsmanager_secret.demo.name
}

output "provider_arn" {
  value = aws_iam_openid_connect_provider.teleport.arn
}
