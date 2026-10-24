# The escalation as a Standard state machine. The property that matters is
# structural and can be read off the definition: Release has exactly one way
# in, from DecideAnswer, and DecideAnswer runs only after AwaitAnswer has been
# completed by a human's answer through SendTaskSuccess. A denial, a timeout,
# an unrecognized decision, an attention-gate verdict, and an exhausted ladder
# each end in their own terminal state and release nothing. Each has a
# receipt: attempt writes the verdict and exhaustion receipts itself, and
# reply writes the rest.
#
# Every Lambda task retries Lambda.TooManyRequestsException and nothing else.
# That retries the invocation, not the delivery: Lambda refused the call
# before the function ran, so no provider was touched and nothing was
# written. Retrying a delivery still belongs to the dispatcher and the breaker
# inside attempt. Lambda.ServiceException is not retried, because it can
# arrive after the function ran, and a re-run Attempt is a second page for
# the same event, with a new link that kills the first.
#
# A task that still fails is caught. Its stage is named by a Pass state, and
# RecordBroken has reply write a FAILED receipt saying which stage broke and
# that nothing was released, so the execution never ends after PROMISED with
# nothing saying why. RecordBroken retries throttling like any task. If it
# cannot be invoked at all, nothing can be signed, and the execution fails
# with the receipt missing; the README names that limit.

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

  # About two minutes of backoff in all: 2, 4, 8, 16, 32, 64 seconds.
  invoke_retry = [
    {
      ErrorEquals     = ["Lambda.TooManyRequestsException"]
      IntervalSeconds = 2
      BackoffRate     = 2
      MaxAttempts     = 6
    },
  ]

  # The Lambda tasks whose failure ends the execution with a broken-promise
  # receipt. reply checks the same list before writing a stage into a receipt.
  broken_stages = ["Attempt", "AwaitAnswer", "Release", "Denied", "TimedOut"]

  definition = {
    Comment = "Reachee escalation: walk the ladder, hold for an answer, release only on direction."
    StartAt = "Attempt"
    States = merge({
      # Walks exactly one tier. Execution input is {escalation_id, tier}.
      # attempt returns {status, next_tier?, timeout_seconds}. When the
      # attention gates stop the escalation, status is the verdict; when the
      # last tier fails, attempt has already written the exhaustion receipt.
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
        Retry          = local.invoke_retry
        Catch          = [{ ErrorEquals = ["States.ALL"], ResultPath = "$.failure", Next = "AttemptBroke" }]
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
          {
            Or = [
              { Variable = "$.attempt.outcome.status", StringEquals = "COALESCED" },
              { Variable = "$.attempt.outcome.status", StringEquals = "DEFERRED" },
              { Variable = "$.attempt.outcome.status", StringEquals = "SUPPRESSED" },
            ]
            Next = "NotDelivered"
          },
        ]
        Default = "Exhausted"
      }

      # The attention gates decided not to reach the person now. That is a
      # decision with a receipt, not a failure. A deferral is not re-queued
      # yet: known defect 2.
      NotDelivered = {
        Type = "Succeed"
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
        Retry      = local.invoke_retry
        Catch = [
          {
            ErrorEquals = ["States.Timeout"]
            ResultPath  = "$.timeout"
            Next        = "TimedOut"
          },
          { ErrorEquals = ["States.ALL"], ResultPath = "$.failure", Next = "AwaitAnswerBroke" },
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
        Retry = local.invoke_retry
        Catch = [{ ErrorEquals = ["States.ALL"], ResultPath = "$.failure", Next = "ReleaseBroke" }]
        End   = true
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
        Retry = local.invoke_retry
        Catch = [{ ErrorEquals = ["States.ALL"], ResultPath = "$.failure", Next = "DeniedBroke" }]
        End   = true
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
        Retry = local.invoke_retry
        Catch = [{ ErrorEquals = ["States.ALL"], ResultPath = "$.failure", Next = "TimedOutBroke" }]
        End   = true
      }

      # Reaching nobody is a failure even when every component reported
      # truthfully, so the execution ends failed, not succeeded. Any status
      # the routes above do not recognize lands here too.
      Exhausted = {
        Type  = "Fail"
        Error = "LadderExhausted"
        Cause = "No tier delivered. attempt has written the receipts."
      }
      }, {
      # Names the stage that broke, and passes on only the error's name. The
      # Catch's Cause is free text and can carry a function's error message,
      # so it stays in the execution history and out of the chain.
      for stage in local.broken_stages : "${stage}Broke" => {
        Type = "Pass"
        Parameters = merge({
          "escalation_id.$" = "$.escalation_id"
          stage             = stage
          "error.$"         = "$.failure.Error"
        }, stage == "Attempt" ? { "tier.$" = "States.Format('{}', $.tier)" } : { tier = "" })
        Next = "RecordBroken"
      }
      }, {
      RecordBroken = {
        Type     = "Task"
        Resource = "arn:aws:states:::lambda:invoke"
        Parameters = {
          FunctionName = local.reply_arn
          Payload = {
            action            = "record_broken"
            "escalation_id.$" = "$.escalation_id"
            "stage.$"         = "$.stage"
            "error.$"         = "$.error"
            "tier.$"          = "$.tier"
          }
        }
        Retry = local.invoke_retry
        Next  = "Broken"
      }

      # A stage failed and the receipt says which. Nothing was released, and
      # the execution ends failed, not succeeded.
      Broken = {
        Type  = "Fail"
        Error = "PromiseBroken"
        Cause = "A stage failed after its retries. The chain's last receipt names it."
      }
    })
  }
}

resource "aws_sfn_state_machine" "escalation" {
  name       = local.state_machine_name
  type       = "STANDARD"
  role_arn   = aws_iam_role.sfn.arn
  definition = jsonencode(local.definition)

  depends_on = [aws_iam_role_policy.sfn]
}
