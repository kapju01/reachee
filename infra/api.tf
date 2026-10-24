# POST /raise is called by the agent or the demo page. GET /reply is the link
# in the email and only renders the choice: mail scanners open links before a
# person does, so a GET must consume nothing. POST /reply records the
# decision. GET /escalation/{id} returns one escalation's chain, or with
# ?format=bundle the bundle verify/ consumes. Browser calls are accepted from
# the demo origin only.

resource "aws_apigatewayv2_api" "http" {
  name          = local.name
  protocol_type = "HTTP"

  cors_configuration {
    allow_origins = [local.demo_origin]
    allow_methods = ["GET", "POST"]
    allow_headers = ["content-type"]
    max_age       = 300
  }
}

# Stage-wide limits, shared by every caller. They bound what an unauthenticated
# POST /raise can cost in email and attention, and sit far above what one
# escalation and one person answering it need.
resource "aws_apigatewayv2_stage" "default" {
  api_id      = aws_apigatewayv2_api.http.id
  name        = "$default"
  auto_deploy = true

  default_route_settings {
    throttling_burst_limit = 5
    throttling_rate_limit  = 2
  }
}

locals {
  # Route key to the function that serves it. Two routes share reply, so the
  # function is named explicitly rather than derived from the route.
  routes = {
    "POST /raise" = "raise"
    "GET /reply"  = "reply"
    "POST /reply" = "reply"

    "GET /escalation/{id}" = "status"
  }

  api_functions = toset(values(local.routes))
}

resource "aws_apigatewayv2_integration" "fn" {
  for_each               = local.api_functions
  api_id                 = aws_apigatewayv2_api.http.id
  integration_type       = "AWS_PROXY"
  integration_uri        = aws_lambda_function.fn[each.key].invoke_arn
  payload_format_version = "2.0"
}

resource "aws_apigatewayv2_route" "fn" {
  for_each  = local.routes
  api_id    = aws_apigatewayv2_api.http.id
  route_key = each.key
  target    = "integrations/${aws_apigatewayv2_integration.fn[each.value].id}"
}

# One permission per route, scoped to its own method and path, so the API
# cannot invoke a function through a route that does not belong to it.
resource "aws_lambda_permission" "api" {
  for_each      = local.routes
  statement_id  = "AllowHttpApi-${replace(replace(replace(each.key, " /", "-"), "/", "-"), "/[{}]/", "")}"
  action        = "lambda:InvokeFunction"
  function_name = aws_lambda_function.fn[each.value].function_name
  principal     = "apigateway.amazonaws.com"
  source_arn    = "${aws_apigatewayv2_api.http.execution_arn}/*/${replace(each.key, " ", "")}"
}
