# The escalation as a Standard state machine. The property that matters is
# structural and can be read off the definition: Release has exactly one way
# in, from DecideAnswer, and DecideAnswer runs only after AwaitAnswer has been
# completed by a human's answer through SendTaskSuccess. A denial, a timeout,
# an unrecognized decision, and an exhausted ladder each end in their own
# terminal state that writes a receipt and releases nothing.
#
# No state retries. A retried Attempt is a second page for the same event,
# and retry belongs to the dispatcher and the breaker inside attempt.

data "aws_iam_policy_document" "sfn_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["states.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [local.account_id]
    }
  }
}

resource "aws_iam_role" "sfn" {
  name               = "${local.name}-escalation"
  assume_role_policy = data.aws_iam_policy_document.sfn_assume.json
}

# The state machine invokes attempt and reply, never raise.
data "aws_iam_policy_document" "sfn" {
  statement {
    actions = ["lambda:InvokeFunction"]
    resources = [
      aws_lambda_function.fn["attempt"].arn,
      aws_lambda_function.fn["reply"].arn,
    ]
  }
}

resource "aws_iam_role_policy" "sfn" {
  name   = "${local.name}-escalation"
  role   = aws_iam_role.sfn.id
  policy = data.aws_iam_policy_document.sfn.json
}

locals {
  attempt_arn = aws_lambda_function.fn["attempt"].arn
  reply_arn   = aws_lambda_function.fn["reply"].arn

  definition = {
    Comment = "Reachee escalation: walk the ladder, hold for an answer, release only on direction."
    StartAt = "Attempt"
    States = {
      # Walks exactly one tier. Execution input is {escalation_id, tier}.
      # attempt returns {status, next_tier?, timeout_seconds}.
      Attempt = {
        Type     = "Task"
        Resource = "arn:aws:states:::lambda:invoke"
        Parameters = {
          FunctionName = local.attempt_arn
          Payload = {
            action            = "attempt"
            "escalation_id.$" = "$.escalation_id"
            "tier.$"          = "$.tier"
          }
        }
        ResultSelector = { "outcome.$" = "$.Payload" }
        ResultPath     = "$.attempt"
        Next           = "RouteAttempt"
      }

      RouteAttempt = {
        Type = "Choice"
        Choices = [
          {
            Variable     = "$.attempt.outcome.status"
            StringEquals = "SENT"
            Next         = "AwaitAnswer"
          },
          {
            Variable  = "$.attempt.outcome.next_tier"
            IsPresent = true
            Next      = "NextTier"
          },
        ]
        Default = "Exhausted"
      }

      NextTier = {
        Type = "Pass"
        Parameters = {
          "escalation_id.$" = "$.escalation_id"
          "tier.$"          = "$.attempt.outcome.next_tier"
        }
        Next = "Attempt"
      }

      # Holds until reply calls SendTaskSuccess with the classified answer,
      # or the window closes. reply stores the task token on this call.
      #
      # The window is the delivering tier's timeout from the Heart-Map, which
      # attempt resolves and returns. Execution input cannot set it: Attempt's
      # ResultPath overwrites $.attempt before this state reads it, so a
      # caller cannot shorten the time a person has to decide. A missing or
      # non-positive value fails the execution rather than waiting forever.
      AwaitAnswer = {
        Type               = "Task"
        Resource           = "arn:aws:states:::lambda:invoke.waitForTaskToken"
        TimeoutSecondsPath = "$.attempt.outcome.timeout_seconds"
        Parameters = {
          FunctionName = local.reply_arn
          Payload = {
            action            = "arm"
            "escalation_id.$" = "$.escalation_id"
            "task_token.$"    = "$$.Task.Token"
          }
        }
        ResultPath = "$.answer"
        Catch = [
          {
            ErrorEquals = ["States.Timeout"]
            ResultPath  = "$.timeout"
            Next        = "TimedOut"
          },
        ]
        Next = "DecideAnswer"
      }

      # The single transition into Release. Anything other than an explicit
      # direction, including a malformed or unclassified answer, is not one.
      DecideAnswer = {
        Type = "Choice"
        Choices = [
          {
            Variable     = "$.answer.decision"
            StringEquals = "direction"
            Next         = "Release"
          },
        ]
        Default = "Denied"
      }

      Release = {
        Type     = "Task"
        Resource = "arn:aws:states:::lambda:invoke"
        Parameters = {
          FunctionName = local.reply_arn
          Payload = {
            action            = "release"
            "escalation_id.$" = "$.escalation_id"
            "answer.$"        = "$.answer"
          }
        }
        End = true
      }

      Denied = {
        Type     = "Task"
        Resource = "arn:aws:states:::lambda:invoke"
        Parameters = {
          FunctionName = local.reply_arn
          Payload = {
            action            = "record_denial"
            "escalation_id.$" = "$.escalation_id"
            "answer.$"        = "$.answer"
          }
        }
        End = true
      }

      # A timeout is not consent. It is recorded as a timeout and ends here.
      TimedOut = {
        Type     = "Task"
        Resource = "arn:aws:states:::lambda:invoke"
        Parameters = {
          FunctionName = local.reply_arn
          Payload = {
            action            = "record_timeout"
            "escalation_id.$" = "$.escalation_id"
          }
        }
        End = true
      }

      Exhausted = {
        Type     = "Task"
        Resource = "arn:aws:states:::lambda:invoke"
        Parameters = {
          FunctionName = local.attempt_arn
          Payload = {
            action            = "record_exhausted"
            "escalation_id.$" = "$.escalation_id"
          }
        }
        End = true
      }
    }
  }
}

resource "aws_sfn_state_machine" "escalation" {
  name       = local.state_machine_name
  type       = "STANDARD"
  role_arn   = aws_iam_role.sfn.arn
  definition = jsonencode(local.definition)

  depends_on = [aws_iam_role_policy.sfn]
}
