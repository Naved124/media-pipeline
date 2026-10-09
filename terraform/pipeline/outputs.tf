output "queue_url" {
  description = "QUEUE_URL for the worker."
  value       = aws_sqs_queue.jobs.id
}

output "dlq_url" {
  value = aws_sqs_queue.dlq.id
}

output "input_bucket" {
  description = "INPUT_BUCKET for the worker."
  value       = module.input_bucket.id
}

output "output_bucket" {
  description = "OUTPUT_BUCKET for the worker."
  value       = module.output_bucket.id
}

output "db_endpoint" {
  description = "Host:port for DATABASE_URL."
  value       = aws_db_instance.main.endpoint
}

output "db_master_secret_arn" {
  description = "Secrets Manager secret holding the generated master credentials."
  value       = aws_db_instance.main.master_user_secret[0].secret_arn
}

output "worker_policy_arn" {
  description = "Attach to the worker's IRSA role in phase 4."
  value       = aws_iam_policy.worker.arn
}

output "worker_security_group_id" {
  description = "Attach to whatever runs the worker; the database only accepts this group."
  value       = aws_security_group.worker.id
}

output "private_subnet_ids" {
  value = aws_subnet.private[*].id
}
