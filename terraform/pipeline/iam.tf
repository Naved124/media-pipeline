# Least-privilege permissions for the worker: read uploads, write renditions,
# receive and delete its own queue's messages, nothing else. Created now so the
# grant is reviewed alongside the resources it names; phase 4 attaches it to
# the worker's IRSA role.

data "aws_iam_policy_document" "worker" {
  statement {
    sid = "ConsumeJobs"
    actions = [
      "sqs:ReceiveMessage",
      "sqs:DeleteMessage",
      "sqs:GetQueueAttributes",
    ]
    resources = [aws_sqs_queue.jobs.arn]
  }

  statement {
    sid       = "ReadUploads"
    actions   = ["s3:GetObject"]
    resources = ["${module.input_bucket.arn}/*"]
  }

  statement {
    sid       = "WriteRenditions"
    actions   = ["s3:PutObject"]
    resources = ["${module.output_bucket.arn}/*"]
  }
}

resource "aws_iam_policy" "worker" {
  name        = "${var.project}-worker"
  description = "Media pipeline worker: consume the job queue, read input, write output"
  policy      = data.aws_iam_policy_document.worker.json
}
