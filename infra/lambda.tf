# Each function gets its own role, log group, and policy, so a compromise of
# one handler grants only what that handler needs.

resource "aws_cloudwatch_log_group" "fn" {
  for_each          = local.functions
  name              = "/aws/lambda/${local.name}-${each.key}"
  retention_in_days = 14
}

data "aws_iam_policy_document" "lambda_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["lambda.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "fn" {
  for_each           = local.functions
  name               = "${local.name}-${each.key}"
  assume_role_policy = data.aws_iam_policy_document.lambda_assume.json
}

locals {
  # Every receipt is signed when written, so all three read the key. Logs
  # are scoped to the function's own group rather than logs:*.
  common_statements = {
    for fn in local.functions : fn => [
      {
        actions   = ["logs:CreateLogStream", "logs:PutLogEvents"]
        resources = ["${aws_cloudwatch_log_group.fn[fn].arn}:*"]
      },
      {
        actions   = ["secretsmanager:GetSecretValue"]
        resources = [aws_secretsmanager_secret.signing_key.arn]
      },
    ]
  }

  own_statements = {
    # raise writes the first receipt and starts the execution.
    raise = [
      {
        actions   = ["dynamodb:PutItem"]
        resources = [aws_dynamodb_table.escalations.arn]
      },
      {
        actions   = ["states:StartExecution"]
        resources = [local.state_machine_arn]
      },
    ]

    # attempt reads the chain head to append to it, and sends on SES.
    # GetAccount has no resource type, so the health check needs "*".
    attempt = [
      {
        actions   = ["dynamodb:Query", "dynamodb:PutItem"]
        resources = [aws_dynamodb_table.escalations.arn]
      },
      {
        actions   = ["ses:SendEmail"]
        resources = local.ses_identity_arns
      },
      {
        actions   = ["ses:GetAccount"]
        resources = ["*"]
      },
    ]

    # reply holds the task token, consumes the single-use reply token, and
    # hands the answer back to the execution. SendTaskSuccess has no resource
    # type in IAM, so it cannot be narrowed below "*"; the task token itself
    # is the control. SendTaskFailure is not granted: a denial is an answer,
    # routed by the state machine, not a failure.
    reply = [
      {
        actions   = ["dynamodb:GetItem", "dynamodb:Query", "dynamodb:PutItem", "dynamodb:UpdateItem"]
        resources = [aws_dynamodb_table.escalations.arn]
      },
      {
        actions   = ["states:SendTaskSuccess"]
        resources = ["*"]
      },
    ]
  }

  env = {
    raise = {
      REACHEE_AUDIT_TABLE   = aws_dynamodb_table.escalations.name
      REACHEE_SIGNING_KEY   = aws_secretsmanager_secret.signing_key.arn
      REACHEE_STATE_MACHINE = local.state_machine_arn
    }
    attempt = {
      REACHEE_AUDIT_TABLE = aws_dynamodb_table.escalations.name
      REACHEE_SIGNING_KEY = aws_secretsmanager_secret.signing_key.arn
      REACHEE_SENDER      = var.sender
      REACHEE_RECIPIENT   = var.recipient
    }
    reply = {
      REACHEE_AUDIT_TABLE = aws_dynamodb_table.escalations.name
      REACHEE_SIGNING_KEY = aws_secretsmanager_secret.signing_key.arn
    }
  }

  timeouts = {
    raise   = 10
    attempt = 30
    reply   = 10
  }
}

data "aws_iam_policy_document" "fn" {
  for_each = local.functions

  dynamic "statement" {
    for_each = concat(local.common_statements[each.key], local.own_statements[each.key])
    content {
      actions   = statement.value.actions
      resources = statement.value.resources
    }
  }
}

resource "aws_iam_role_policy" "fn" {
  for_each = local.functions
  name     = "${local.name}-${each.key}"
  role     = aws_iam_role.fn[each.key].id
  policy   = data.aws_iam_policy_document.fn[each.key].json
}

resource "aws_lambda_function" "fn" {
  for_each = local.functions

  function_name    = "${local.name}-${each.key}"
  role             = aws_iam_role.fn[each.key].arn
  runtime          = "provided.al2023"
  architectures    = ["arm64"]
  handler          = "bootstrap"
  filename         = "${path.module}/../build/${each.key}.zip"
  source_code_hash = filebase64sha256("${path.module}/../build/${each.key}.zip")
  memory_size      = 128
  timeout          = local.timeouts[each.key]

  environment {
    variables = local.env[each.key]
  }

  depends_on = [aws_cloudwatch_log_group.fn, aws_iam_role_policy.fn]
}
