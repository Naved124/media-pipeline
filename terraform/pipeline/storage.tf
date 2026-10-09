# Input and output are separate buckets so a misconfigured worker can never
# write a rendition back into the bucket that triggers it.

# Originals are the only data in the system that can't be regenerated, so
# they are versioned against accidental overwrite or delete.
module "input_bucket" {
  source = "../modules/private-bucket"

  name          = var.input_bucket_name
  force_destroy = var.force_destroy_buckets
  versioning    = true
}

# Renditions are derived and written to deterministic keys, so they can always
# be rebuilt from the input; versioning would only keep stale copies.
module "output_bucket" {
  #checkov:skip=CKV_AWS_21:Derived, reproducible data; see comment above.
  source = "../modules/private-bucket"

  name          = var.output_bucket_name
  force_destroy = var.force_destroy_buckets
  versioning    = false
}

resource "aws_s3_bucket_notification" "input" {
  bucket = module.input_bucket.id

  queue {
    queue_arn = aws_sqs_queue.jobs.arn
    events    = ["s3:ObjectCreated:*"]
  }

  # S3 validates the destination when the notification is created, which
  # fails unless the queue policy already lets the bucket send to it.
  depends_on = [aws_sqs_queue_policy.jobs]
}
