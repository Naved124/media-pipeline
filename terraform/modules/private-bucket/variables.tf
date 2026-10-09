variable "name" {
  description = "Globally unique bucket name."
  type        = string
}

variable "force_destroy" {
  description = "Allow terraform destroy to delete a non-empty bucket. Keep false outside throwaway environments."
  type        = bool
  default     = false
}

variable "versioning" {
  description = "Keep previous object versions."
  type        = bool
  default     = true
}

variable "noncurrent_version_days" {
  description = "Days to keep a superseded object version when versioning is on."
  type        = number
  default     = 30
}

variable "policy_documents" {
  description = "Extra IAM policy JSON documents merged into the bucket policy."
  type        = list(string)
  default     = []
}
