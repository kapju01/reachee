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
  # Every receipt is signed when written, so the signers read the key; status
  # writes none and cannot. Logs are scoped to the function's own group
  # rather than logs:*.
  common_statements = {
    for fn in local.functions : fn => concat(
      [{
        actions   = ["logs:CreateLogStream", "logs:PutLogEvents"]
        resources = ["${aws_cloudwatch_log_group.fn[fn].arn}:*"]
      }],
      contains(local.signers, fn) ? [{
        actions   = ["secretsmanager:GetSecretValue"]
        resources = [aws_secretsmanager_secret.signing_key.arn]
      }] : [],
    )
  }

  own_statements = {
    # raise writes the request record and the PROMISED receipt, reading the
    # (empty) chain first so the logger finds its head the one way it always
    # does, and starts the execution.
    raise = [
      {
        actions   = ["dynamodb:Query", "dynamodb:PutItem"]
        resources = [aws_dynamodb_table.escalations.arn]
      },
      {
        actions   = ["states:StartExecution"]
        resources = [local.state_machine_arn]
      },
    ]

    # attempt reads the request record and the chain head, mints the reply
    # slot, appends to the chain, and sends on SES. GetAccount has no resource type, so the health
    # check needs "*".
    attempt = [
      {
        actions   = ["dynamodb:GetItem", "dynamodb:Query", "dynamodb:PutItem"]
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

    # reply holds the task token, spends the link's nonce, writes the answer
    # receipts, and hands the answer back to the execution. Every slot change
    # is a conditional PutItem, so UpdateItem is not granted. SendTaskSuccess
    # has no resource type in IAM, so it cannot be narrowed below "*"; the
    # task token itself is the control. SendTaskFailure is not granted: a
    # denial is an answer, routed by the state machine, not a failure.
    reply = [
      {
        actions   = ["dynamodb:GetItem", "dynamodb:Query", "dynamodb:PutItem"]
        resources = [aws_dynamodb_table.escalations.arn]
      },
      {
        actions   = ["states:SendTaskSuccess"]
        resources = ["*"]
      },
    ]

    # status is reachable by anyone holding an escalation id, so its role is
    # the boundary, not its code. The request record and the reply slot share
    # the chain's partition, and an unconditioned Query would return the
    # payload, subject, note, nonce, and task token. These conditions grant
    # Query only when it names the event attribute alone and says so with
    # Select; ForAllValues passes on an absent key, so Select is tested
    # without IfExists and a query that omits it is refused.
    status = [
      {
        actions   = ["dynamodb:Query"]
        resources = [aws_dynamodb_table.escalations.arn]
        conditions = [
          {
            test     = "ForAllValues:StringEquals"
            variable = "dynamodb:Attributes"
            values   = ["escalation_id", "seq", "event"]
          },
          {
            test     = "StringEquals"
            variable = "dynamodb:Select"
            values   = ["SPECIFIC_ATTRIBUTES"]
          },
        ]
      },
    ]
  }

  env = {
    raise = {
      REACHEE_AUDIT_TABLE   = aws_dynamodb_table.escalations.name
      REACHEE_SIGNING_KEY   = aws_secretsmanager_secret.signing_key.arn
      REACHEE_STATE_MACHINE = local.state_machine_arn
      REACHEE_HEARTMAP_DOC  = local.heartmap
    }
    attempt = {
      REACHEE_AUDIT_TABLE  = aws_dynamodb_table.escalations.name
      REACHEE_SIGNING_KEY  = aws_secretsmanager_secret.signing_key.arn
      REACHEE_SENDER       = var.sender
      REACHEE_HEARTMAP_DOC = local.heartmap
      REACHEE_REPLY_URL    = "${aws_apigatewayv2_api.http.api_endpoint}/reply"
    }
    reply = {
      REACHEE_AUDIT_TABLE = aws_dynamodb_table.escalations.name
      REACHEE_SIGNING_KEY = aws_secretsmanager_secret.signing_key.arn
    }
    # The same file web.tf publishes, so a bundle names the key a verifier
    # will fetch.
    status = {
      REACHEE_AUDIT_TABLE = aws_dynamodb_table.escalations.name
      REACHEE_PUBLIC_KEY  = trimspace(file("${path.module}/../demo/keys/reachee-ed25519.pub"))
    }
  }

  timeouts = {
    raise   = 10
    attempt = 30
    reply   = 10
    status  = 10
  }
}

data "aws_iam_policy_document" "fn" {
  for_each = local.functions

  dynamic "statement" {
    for_each = concat(local.common_statements[each.key], local.own_statements[each.key])
    content {
      actions   = statement.value.actions
      resources = statement.value.resources

      dynamic "condition" {
        for_each = lookup(statement.value, "conditions", [])
        content {
          test     = condition.value.test
          variable = condition.value.variable
          values   = condition.value.values
        }
      }
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
