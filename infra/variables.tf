variable "sender" {
  description = "SES-verified address the escalation email is sent from."
  type        = string
}

variable "recipient" {
  description = "The one SES-verified recipient this build reaches."
  type        = string
}

variable "demo_bucket" {
  description = "Globally unique name for the private bucket that holds the demo page."
  type        = string
}
