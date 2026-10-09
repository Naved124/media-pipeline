variable "region" {
  description = "AWS region for the state bucket."
  type        = string
  default     = "ap-south-1"
}

variable "state_bucket_name" {
  description = "Globally unique name for the Terraform state bucket. Supplied via terraform.tfvars, never committed."
  type        = string
}

variable "force_destroy" {
  description = "Allow destroying the state bucket while it still holds state. Only for throwaway environments."
  type        = bool
  default     = false
}
