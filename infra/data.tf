# One table holds every escalation's receipts, keyed by escalation and
# ordered by sequence number within it. On-demand billing means no idle cost.
resource "aws_dynamodb_table" "escalations" {
  name         = "${local.name}-escalations"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "escalation_id"
  range_key    = "seq"

  attribute {
    name = "escalation_id"
    type = "S"
  }

  attribute {
    name = "seq"
    type = "N"
  }
}

# The signing key's container only. There is deliberately no
# aws_secretsmanager_secret_version: the key is put in out of band so it
# never passes through Terraform state.
resource "aws_secretsmanager_secret" "signing_key" {
  name        = "${local.name}/signing-key"
  description = "Ed25519 key that signs Reachee receipts. Value set out of band."

  # Deleted immediately on destroy, so a destroy-and-reapply does not fail on
  # a name still held for recovery. The cost is that a destroyed key is gone;
  # the published public key still verifies chains already exported.
  recovery_window_in_days = 0
}
