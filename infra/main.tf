# Reachee root module. One table, one secret container, three Lambdas, one
# Standard state machine, one HTTP API, and the demo bucket behind
# CloudFront. Nothing here is multi-tenant or highly available, by design.

terraform {
  required_version = ">= 1.6"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
  }
}

# The region is a variable only so env.sh can hand it in. The validation
# makes it a constant in practice: every ARN, identity, and claim in the
# README is in us-west-2.
variable "region" {
  description = "AWS region. Must be us-west-2."
  type        = string
  default     = "us-west-2"

  validation {
    condition     = var.region == "us-west-2"
    error_message = "Reachee is built and evidenced in us-west-2 only."
  }
}

provider "aws" {
  region = var.region

  default_tags {
    tags = {
      Project = "reachee"
    }
  }
}

data "aws_caller_identity" "current" {}

locals {
  name       = "reachee"
  account_id = data.aws_caller_identity.current.account_id

  functions = toset(["raise", "attempt", "reply"])

  # The declared contact policy, rendered with the one verified recipient so
  # the real address lives in tfvars and never in the repo. It is the same
  # shape as examples/heartmap.yaml.
  heartmap = templatefile("${path.module}/heartmap.yaml.tftpl", { recipient = var.recipient })

  demo_bucket = coalesce(var.demo_bucket, "reachee-demo-${local.account_id}")

  # Built from the name rather than read from the resource: raise needs the
  # ARN and the state machine needs raise's siblings, and all three functions
  # are one for_each, so a resource reference would be a cycle.
  state_machine_name = "${local.name}-escalation"
  state_machine_arn  = "arn:aws:states:${var.region}:${local.account_id}:stateMachine:${local.state_machine_name}"

  # The only origin the API accepts browser calls from. It is derived from
  # the distribution itself so it cannot drift from what is deployed.
  demo_origin = "https://${aws_cloudfront_distribution.demo.domain_name}"

  # Sandbox SES authorizes SendEmail against both the sender and the
  # recipient identity, so the attempt role names both and nothing else.
  ses_identity_arns = [
    "arn:aws:ses:${var.region}:${local.account_id}:identity/${var.sender}",
    "arn:aws:ses:${var.region}:${local.account_id}:identity/${var.recipient}",
  ]
}
