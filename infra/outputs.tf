output "api_endpoint" {
  description = "Base URL of the HTTP API. POST /raise and GET /reply hang off it."
  value       = aws_apigatewayv2_api.http.api_endpoint
}

output "demo_url" {
  description = "HTTPS URL of the demo page."
  value       = "${local.demo_origin}/"
}

output "pubkey_url" {
  description = "URL to pass the verifier as --pubkey."
  value       = "${local.demo_origin}/keys/reachee-ed25519.pub"
}

output "table_name" {
  description = "DynamoDB table holding the receipts."
  value       = aws_dynamodb_table.escalations.name
}
