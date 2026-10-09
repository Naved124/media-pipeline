# Postgres for job metadata. Private subnets only, reachable only from the
# worker security group, TLS enforced server-side, and the master password is
# generated and held by Secrets Manager, so it never appears in state or code.

resource "aws_db_subnet_group" "main" {
  name       = var.project
  subnet_ids = aws_subnet.private[*].id
}

resource "aws_db_parameter_group" "main" {
  name   = "${var.project}-postgres16"
  family = "postgres16"

  parameter {
    name         = "rds.force_ssl"
    value        = "1"
    apply_method = "pending-reboot"
  }

  parameter {
    name  = "log_connections"
    value = "1"
  }

  parameter {
    name  = "log_disconnections"
    value = "1"
  }
}

resource "aws_db_instance" "main" {
  #checkov:skip=CKV_AWS_118:Enhanced monitoring lands with observability (phase 7).
  #checkov:skip=CKV_AWS_353:Performance Insights lands with observability (phase 7).
  #checkov:skip=CKV2_AWS_30:Query logging lands with observability (phase 7).
  identifier     = var.project
  engine         = "postgres"
  engine_version = "16"
  instance_class = var.db_instance_class

  allocated_storage     = 20
  max_allocated_storage = 100
  storage_type          = "gp3"
  storage_encrypted     = true

  db_name                     = "media_pipeline"
  username                    = "media_pipeline_admin"
  manage_master_user_password = true

  iam_database_authentication_enabled = true

  multi_az               = var.db_multi_az
  db_subnet_group_name   = aws_db_subnet_group.main.name
  vpc_security_group_ids = [aws_security_group.db.id]
  publicly_accessible    = false
  parameter_group_name   = aws_db_parameter_group.main.name

  backup_retention_period    = 7
  copy_tags_to_snapshot      = true
  auto_minor_version_upgrade = true
  deletion_protection        = var.db_deletion_protection
  skip_final_snapshot        = var.db_skip_final_snapshot
  final_snapshot_identifier  = var.db_skip_final_snapshot ? null : "${var.project}-final"

  enabled_cloudwatch_logs_exports = ["postgresql", "upgrade"]
}
