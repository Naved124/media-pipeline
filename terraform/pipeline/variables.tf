variable "region" {
  description = "AWS region."
  type        = string
  default     = "ap-south-1"
}

variable "project" {
  description = "Name prefix and Project tag for every resource."
  type        = string
  default     = "media-pipeline"
}

# --- network ---

variable "vpc_cidr" {
  description = "VPC CIDR. Public subnets take /24s from the bottom /20; private subnets take the /20s above it."
  type        = string
  default     = "10.0.0.0/16"
}

variable "az_count" {
  description = "Number of availability zones to spread subnets across."
  type        = number
  default     = 3

  validation {
    condition     = var.az_count >= 2
    error_message = "Multi-AZ RDS needs subnets in at least two availability zones."
  }
}

variable "flow_log_retention_days" {
  description = "CloudWatch retention for VPC flow logs."
  type        = number
  default     = 365
}

# --- storage ---

variable "input_bucket_name" {
  description = "Globally unique name of the bucket uploads land in. Supplied via terraform.tfvars."
  type        = string
}

variable "output_bucket_name" {
  description = "Globally unique name of the bucket renditions are written to. Supplied via terraform.tfvars."
  type        = string

  validation {
    condition     = var.output_bucket_name != var.input_bucket_name
    error_message = "Input and output must be separate buckets, or the worker retriggers itself on its own output."
  }
}

variable "force_destroy_buckets" {
  description = "Allow terraform destroy to delete non-empty buckets. Throwaway environments only."
  type        = bool
  default     = false
}

# --- queue ---

variable "visibility_timeout_seconds" {
  description = "How long a received message stays hidden. Must exceed the worker's per-job timeout (330s)."
  type        = number
  default     = 360

  validation {
    condition     = var.visibility_timeout_seconds > 330
    error_message = "The visibility timeout must be longer than the worker's 330s job timeout, or a slow job is redelivered while still running."
  }
}

variable "max_receive_count" {
  description = "Deliveries before a message moves to the dead-letter queue."
  type        = number
  default     = 5
}

# --- database ---

variable "db_instance_class" {
  description = "RDS instance class."
  type        = string
  default     = "db.t4g.micro"
}

variable "db_multi_az" {
  description = "Run RDS with a synchronous standby in a second AZ."
  type        = bool
  default     = true
}

variable "db_deletion_protection" {
  description = "Block deletion of the RDS instance."
  type        = bool
  default     = true
}

variable "db_skip_final_snapshot" {
  description = "Skip the final snapshot when the RDS instance is destroyed. Throwaway environments only."
  type        = bool
  default     = false
}
