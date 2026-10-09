# media-pipeline

An event-driven media processing pipeline on AWS: upload a video to S3, and a fleet of
Go worker pods on EKS — scaled from zero by queue depth — transcodes it into multiple
resolutions and publishes the results behind CloudFront.

This repository is built in public, phase by phase, as a study in production-grade
platform engineering rather than a "deploy an app to the cloud" demo. The interesting
parts are the ones that only show up under load and failure: scale-to-zero, message
redelivery, idempotency, chaos testing, and cost per unit of work.

> **Status:** in active development. See [Build status](#build-status) for exactly what
> works today and what is still scaffolding.

---

## Table of contents

- [Architecture](#architecture)
- [Build status](#build-status)
- [Repository layout](#repository-layout)
- [The worker](#the-worker)
- [Data model](#data-model)
- [Configuration](#configuration)
- [Running locally](#running-locally)
- [Infrastructure](#infrastructure)
- [Failure handling](#failure-handling)
- [Design decisions](#design-decisions)
- [Well-Architected review](#well-architected-review)
- [Roadmap](#roadmap)
- [Maintaining this document](#maintaining-this-document)

---

## Architecture

```
                 ┌──────────────┐
   upload  ──▶   │  S3 (input)  │
                 └──────┬───────┘
                        │  s3:ObjectCreated:*
                        ▼
                 ┌──────────────┐        ┌──────────────┐
                 │     SQS      │───────▶│     DLQ      │
                 └──────┬───────┘        └──────────────┘
                        │                   after maxReceiveCount
             queue depth │ (KEDA scaler)
                        ▼
              ┌──────────────────────┐
              │   EKS worker pods    │     Go + ffmpeg
              │   replicas 0 ─▶ N    │     one message = one job
              └───┬──────────────┬───┘
                  │              │
       job metadata│              │ renditions
                  ▼              ▼
          ┌──────────────┐  ┌──────────────┐     ┌──────────────┐
          │ RDS Postgres │  │ S3 (output)  │────▶│  CloudFront  │
          │  (Multi-AZ)  │  └──────────────┘     └──────────────┘
          └──────────────┘
```

**Flow.** A client uploads an object to the input bucket. The bucket's event
notification publishes an `s3:ObjectCreated:*` event to SQS. Worker pods long-poll the
queue; each pod that receives a message generates a job ID, records the job, downloads
the source object, probes its container and real height with `ffprobe`, transcodes it into every
target resolution _at or below_ that height, uploads each rendition under
`<job_id>/<resolution>.mp4` in the output bucket, marks the job complete, and only then
deletes the SQS message. CloudFront fronts the output bucket for delivery.

**Why this shape.** The queue is the load-bearing element. It decouples upload rate from
processing rate, survives worker restarts, gives at-least-once delivery for free, and
exposes a single number — queue depth — that KEDA can scale on. Scaling on CPU would be
wrong here: a pod that has just started has no CPU load yet but a hundred pending jobs
say it should exist.

---

## Build status

Ten phases, each producing something runnable before the next begins.

| #   | Phase                                                                           | Status         |
| --- | ------------------------------------------------------------------------------- | -------------- |
| 1   | Local scaffold — worker skeleton, Postgres via compose, schema                  | ✅ done        |
| 2   | Core AWS plumbing — Terraform for VPC, S3, SQS, RDS, remote state               | 🔨 in progress |
| 3   | Worker end-to-end on simplest compute (single container)                        | 🔨 in progress |
| 4   | Move to EKS — Deployment, IRSA, manual replicas                                 | ⬜ not started |
| 5   | KEDA autoscaling — ScaledObject on queue depth, load test, 0→N→0 recording      | ⬜ not started |
| 6   | GitOps delivery — ArgoCD reconciling a manifests repo; CI builds and scans only | ⬜ not started |
| 7   | Observability — Prometheus/Grafana, queue depth vs pod count, one real alert    | ⬜ not started |
| 8   | Security hardening — Kyverno policies, SSM/Secrets Manager, least-privilege IAM | ⬜ not started |
| 9   | Chaos testing — kill node/pod mid-job, simulate AZ loss, measure MTTR           | ⬜ not started |
| 10  | Cost + case study — spot instances, cost per 1000 files, architecture write-up  | ⬜ not started |

### What actually runs today

- ✅ `jobs` table designed, migrated, and verified against local Postgres
- ✅ S3 → SQS event flow proven end-to-end against real AWS (manually provisioned, since torn down)
- ✅ `internal/queue` — long-polling receive; S3 events validated, URL-decoded and reduced to a `Message`; `s3:TestEvent` deleted
- ✅ `internal/storage` — download into a private per-job directory with a size cap, upload to output bucket
- ✅ `internal/transcode` — one `ffprobe` call for container and dimensions, input-format allowlist, downscale-only rendition set, bounded by the job context
- ✅ `internal/db` — row written as `processing` on receipt, then `completed` with `output_keys` or `failed` with `error_message`; refuses unverified TLS to a remote database
- ✅ SQS message deletion — after the `completed` write, never before
- ✅ `worker.processJob` — receive → record → download → transcode → upload → complete → delete, covered by tests against real Postgres and ffmpeg
- ✅ Dockerfile — multi-stage, non-root, digest-pinned
- 🔨 Terraform — bootstrap (state bucket) and pipeline (VPC, buckets, queue + DLQ, RDS, worker IAM policy) written, `validate`d and checkov-clean; **not yet applied**
- ⬜ An end-to-end run against Terraform-provisioned AWS

---

## Repository layout

```
media-pipeline/
├── db/
│   └── migrations/
│       └── 0001_create_jobs_table.sql
├── local/
│   ├── docker-compose.yml          # Postgres for local development (loopback only)
│   ├── .env.example                # copy to .env (gitignored) for the DB password
│   ├── sqs-policy.json             # SQS resource policy template allowing s3.amazonaws.com
│   └── s3-notification.json        # bucket notification config template
├── terraform/
│   ├── bootstrap/                  # remote-state bucket (local state, applied once)
│   ├── pipeline/                   # VPC, S3 pair, SQS + DLQ, RDS, worker IAM policy
│   └── modules/
│       └── private-bucket/         # the one hardened bucket every stack uses
├── worker/
│   ├── cmd/
│   │   └── worker/
│   │       └── main.go             # entrypoint: config, setup, poll loop, processJob
│   ├── internal/
│   │   ├── queue/                  # SQS receive/delete + S3 event validation
│   │   ├── storage/                # S3 download/upload
│   │   ├── transcode/              # ffprobe + ffmpeg
│   │   └── db/                     # connection setup + job status writes
│   ├── Dockerfile
│   ├── go.mod
│   └── go.sum
└── README.md
```

`cmd/` holds the executable and everything about _wiring_; `internal/` holds the
capabilities and knows nothing about how they're composed. `internal/` is enforced by
the Go toolchain — nothing outside this module can import it — which keeps these
packages free to change shape without being a public API.

---

## The worker

### Poll loop (`cmd/worker/main.go`)

The worker is an infinite loop that never exits on its own. Pod lifecycle belongs to
Kubernetes and KEDA, not to the program: a worker that decided to stop when the queue
looked empty would fight its own orchestrator. AWS clients and the database handle are
constructed once, before the loop, not per iteration.

Configuration is validated before anything else: a missing variable, a non-numeric
`MAX_INPUT_BYTES`, or `INPUT_BUCKET == OUTPUT_BUCKET` stops the process at start, so a
misconfigured pod crash-loops visibly instead of failing on its first message.

```
for {
    receive a message (20s long poll)
    no message  → loop
    message     → processJob(msg); log any error and loop
}
```

### `worker.processJob`

All per-job work lives on a `worker` struct that carries the clients as fields, so the
call site stays one line as dependencies accumulate. Every failure path returns an error
wrapped with `%w`; the caller logs it. Nothing inside `processJob` steers the poll loop.

```
reject an event for any bucket but INPUT_BUCKET
CreateJob            → row is 'processing' with started_at
transcodeAndUpload   → download, transcode, upload each rendition
  on error: FailJob  → row is 'failed' with error_message; message NOT deleted
CompleteJob          → row is 'completed' with output_keys
Delete               → only now is the message removed from the queue
```

`FailJob` runs on a context detached from the job's: when the job failed _because_ its
5m30s deadline expired, the job context is already dead, and the failure would otherwise
go unrecorded. Each job works in its own `os.MkdirTemp` directory (mode 0700), removed
when the job returns however it returns.

### `internal/queue`

Receives one message at a time with a 20-second long poll — which is both cheaper than
short polling and the reason the loop doesn't spin at 100% CPU on an empty queue. The
raw S3 event JSON is unmarshalled into throwaway structs and reduced to a `Message`
carrying only what the rest of the system needs: the object key and the receipt handle.

The receipt handle is the claim ticket for deleting that specific delivery. SQS does not
delete on receive; if the message is never explicitly deleted it becomes visible again
when the visibility timeout expires. That property is the retry mechanism and the
reason idempotency matters.

The message body is untrusted input, so `parseEvent` accepts exactly one record with
`eventSource` `aws:s3` and an `ObjectCreated:*` event name, and rejects anything else
rather than guessing. Two details only show up against real S3:

- Saving a bucket notification makes S3 publish an `s3:TestEvent` — a different shape
  with no `Records` at all. Indexing `Records[0]` on it panicked the worker. It is now
  recognised and deleted.
- Object keys arrive URL-encoded: `my video.mp4` is sent as `my+video.mp4`. Used raw,
  every key with a space or non-ASCII character 404s on download.

### `internal/storage`

Wraps one S3 client and two bucket names — input and output are separate buckets, so
that a misconfigured worker cannot write derived files back into the source bucket and
retrigger itself. `Download` streams the object body into the job's directory and returns
the local path; `Upload` streams a local file to a given key and returns only an error,
since it produces nothing the caller needs back.

The local file is always named `source` — nothing from the object key reaches the
filesystem — and is opened `O_EXCL` with mode 0600, so it can't follow a planted symlink
or collide with another job. Downloads over `MAX_INPUT_BYTES` are refused by
`Content-Length` and, in case that is missing or wrong, by counting the bytes actually
read.

### `internal/transcode`

Probes the source's container and real dimensions with one `ffprobe` call before doing
anything:

```
ffprobe -v error -protocol_whitelist file -select_streams v:0 \
        -show_entries stream=width,height:format=format_name -of json <file>
```

The container must be on an allowlist — `mov,mp4,…` or `matroska,webm` — and ffmpeg is
then pinned to that demuxer with `-f`. Some formats ffmpeg auto-detects are not media
but instructions: an HLS or concat playlist uploaded as `video.mp4` makes ffmpeg open
other files or URLs named inside it, which turns an upload into a local-file read or
SSRF. Newer ffmpeg builds block some of these on their own; the allowlist doesn't depend
on which build the image ships. `-protocol_whitelist file` keeps both tools off the
network, frames outside 1–8192 px are refused, and both run under the job context with
`exec.CommandContext`, so the job timeout really stops a hung decode.

Targets are `{1080, 720, 480, 360}`, stored as integers so the "is this target smaller
than the source" test is a numeric comparison. Anything larger than the source height is
skipped — upscaling burns CPU and storage to produce a file that is strictly worse than
the original. The result is a variable-length set of renditions, which falls out of that
filter rather than being special-cased.

Probing rather than trusting filenames is not paranoia: a file labelled "1080p" in
testing probed at 818 pixels tall, because it was a 2.35:1 cinematic crop.

### `internal/db`

`Connect` builds the pool once and pings it before the poll loop starts. It refuses any
database host that isn't loopback or a unix socket unless the URL says
`sslmode=verify-full`: pgx's default, `prefer`, never checks the server certificate and
falls back to plaintext without saying so.

`CompleteJob` and `FailJob` only move a row that is still `processing`, and report an
error if there is no such row, so a terminal job can't be overwritten. `error_message`
is truncated to 4 KiB and stripped of NUL bytes and invalid UTF-8, which Postgres
rejects in `TEXT` and ffmpeg's stderr can contain.

---

## Data model

`db/migrations/0001_create_jobs_table.sql`

| column          | type            | notes                                                                         |
| --------------- | --------------- | ----------------------------------------------------------------------------- |
| `job_id`        | `TEXT` PK       | UUID generated by the worker                                                  |
| `status`        | `TEXT NOT NULL` | `CHECK` enum: `pending`, `processing`, `completed`, `failed`, `dead_lettered` |
| `input_key`     | `TEXT NOT NULL` | source object key                                                             |
| `output_keys`   | `JSONB`         | labelled `{resolution, key}` entries, null until complete                     |
| `created_at`    | `TIMESTAMPTZ`   | defaults to `now()`                                                           |
| `started_at`    | `TIMESTAMPTZ`   | null until picked up                                                          |
| `completed_at`  | `TIMESTAMPTZ`   | null until finished                                                           |
| `error_message` | `TEXT`          | null unless failed                                                            |

**The worker owns job identity.** An S3 event carries a bucket and an object key and
nothing else — there is no job ID anywhere upstream. The first worker to see a message
generates one. This is why the row must be written _on receipt_, not on success: a pod
killed mid-transcode leaves a row stuck in `processing` with a `started_at` and no
`completed_at`, which is a queryable signal:

```sql
select * from jobs
 where status = 'processing'
   and started_at < now() - interval '30 minutes';
```

That query is the stuck-job alert in phase 7. If the row were written on success
instead, a job that had silently failed and redelivered five times would be
indistinguishable from one that was never submitted.

**`output_keys` is JSONB, not an array**, because a consumer needs to know _which_
rendition each URL is, and the set is variable-length by design. Entries hold the S3
key (`[{"resolution":"720p","key":"<job_id>/720p.mp4"}]`), not a URL — see D13.

---

## Configuration

All configuration is read from the environment — the same image runs locally, on a VM,
and in a Kubernetes Deployment, with values injected per environment. Nothing is
hardcoded and nothing is baked into the image.

| variable          | meaning                                                           |
| ----------------- | ----------------------------------------------------------------- |
| `QUEUE_URL`       | full SQS queue URL (the SQS API genuinely takes a URL)            |
| `INPUT_BUCKET`    | source bucket **name** — S3 addresses buckets by name, not URL    |
| `OUTPUT_BUCKET`   | destination bucket **name**; must differ from `INPUT_BUCKET`      |
| `DATABASE_URL`    | Postgres connection string; `sslmode=verify-full` unless local    |
| `MAX_INPUT_BYTES` | optional; largest upload the worker will download (default 2 GiB) |

For RDS, `verify-full` needs the RDS CA bundle, which is not in the image's system
roots: download `global-bundle.pem` from AWS and pass it as `sslrootcert=<path>` in
`DATABASE_URL`. In phase 4 it is mounted into the pod rather than baked into the image.

AWS credentials are resolved through the default credential chain: environment
variables and shared config locally, IRSA in EKS. No static keys are ever mounted into
a pod.

---

## Running locally

Requirements: Go 1.27+, Docker, `ffmpeg` and `ffprobe` on `PATH`, AWS credentials with
access to an S3 bucket pair and an SQS queue.

```bash
# Postgres — choose a password first; compose refuses to start without one
cd local && cp .env.example .env && $EDITOR .env
docker compose up -d
export DATABASE_URL="postgres://postgres:<password>@localhost:5432/media_pipeline?sslmode=disable"

# schema
psql "$DATABASE_URL" -f ../db/migrations/0001_create_jobs_table.sql

# worker
cd ../worker
go build ./...
QUEUE_URL=... INPUT_BUCKET=... OUTPUT_BUCKET=... go run ./cmd/worker

# tests — the db and processJob tests need a database and skip without one;
# the transcode tests need ffmpeg and skip without it
TEST_DATABASE_URL="$DATABASE_URL" go test ./...

# image
docker build -t media-pipeline-worker ./
```

`sslmode=disable` is accepted only because the host is `localhost`; see
[Configuration](#configuration).

There is deliberately no LocalStack. As of March 2026 it requires an account and auth
token, and the no-account alternatives were not trustworthy enough to run against. The
S3 and SQS surface this project uses is small, so it is tested against real AWS.

---

## Infrastructure

Two Terraform stacks, applied in order. Every bucket, queue and state name is supplied
through gitignored `terraform.tfvars` / `backend.tfbackend` files — copy the `.example`
next to each.

```bash
# once per account: the state bucket (local state)
cd terraform/bootstrap && terraform init && terraform apply

# the pipeline, with state in that bucket
cd ../pipeline
terraform init -backend-config=backend.tfbackend
terraform apply
```

What `pipeline` builds, and why each piece is the way it is:

- **VPC** across three AZs, public and private subnets, a free S3 gateway endpoint, flow
  logs, and a default security group stripped of all rules. No NAT gateway yet (D11).
- **Buckets** from one `private-bucket` module: public access blocked, SSE, owner-enforced
  object ownership, a policy that denies non-TLS requests, and a lifecycle rule that
  aborts abandoned multipart uploads. The input bucket is versioned — originals are the
  only data that can't be regenerated; the output bucket isn't.
- **Queue + DLQ**: 360s visibility (a variable validation enforces > the worker's 330s
  job timeout), 20s long polling, SSE, `maxReceiveCount` 5. The queue policy admits only
  `s3.amazonaws.com`, only for the input bucket's ARN, and only from this account
  (`aws:SourceAccount` — bucket names are global, so ARN alone isn't enough).
- **RDS Postgres 16** in private subnets, Multi-AZ, encrypted, not publicly accessible,
  `rds.force_ssl = 1`, reachable only from the `worker` security group. The master
  password is generated and held by Secrets Manager (`manage_master_user_password`), so
  it never appears in code or state.
- **Worker IAM policy**: receive/delete on the job queue, `GetObject` on input,
  `PutObject` on output, nothing else. Attached to the IRSA role in phase 4.

Both stacks `validate` and pass checkov; each skipped check carries its reason inline.

---

## Failure handling

The contract is at-least-once delivery with the message deleted only after the job is
durably complete. Concretely:

| failure                          | behaviour                                                               |
| -------------------------------- | ----------------------------------------------------------------------- |
| receive fails                    | log, continue polling                                                   |
| `s3:TestEvent`                   | deleted on receipt; it is not a job                                     |
| malformed or foreign message     | no row written, not deleted, reaches the DLQ via redrive                |
| download fails or is oversized   | row `failed`, message not deleted, redelivered after visibility timeout |
| unsupported container / no video | row `failed` with the reason, message not deleted, redelivered          |
| `ffprobe` / `ffmpeg` fails       | row `failed` with its output, message not deleted, redelivered          |
| job exceeds 5m30s                | ffmpeg killed, row `failed`, message redelivered                        |
| any upload fails                 | row `failed`, remaining renditions abandoned, message redelivered       |
| `completed` write fails          | message not deleted, redelivered; row left in `processing`              |
| delete fails after `completed`   | logged; the message redelivers and the job runs again under a new ID    |
| pod killed mid-job               | no delete happened, so the message reappears; row left in `processing`  |
| repeated failure                 | `maxReceiveCount` exceeded → DLQ → job marked `dead_lettered`           |

Each redelivery is a new job ID and a new row, so a job that failed five times shows
up as five `failed` rows with five reasons. Marking DLQ'd jobs `dead_lettered` is not
built yet.

The alternative for a failed rendition — log it and carry on — was considered and
rejected: it produces a job marked `completed` with a permanently missing resolution and
nothing anywhere to alert on. A loud failure that retries beats a quiet success that
lies.

**Idempotency is an open item.** Because redelivery is guaranteed and concurrent pods
are the point, re-processing must be safe. Writing renditions to deterministic keys
under a job prefix makes the S3 side naturally overwrite-safe, but the database path and
the visibility-timeout margin for long transcodes both need deliberate handling. This
gets proven in phase 4, before autoscaling is added, and attacked directly in phase 9.

---

## Design decisions

An append-only log. New entries go at the bottom; existing entries are not rewritten,
only superseded by later ones that say so.

**D1 — Second project, not an extension of the first.**
Deliberately separate from `cloud-event-pipeline`. That project demonstrates provisioning
and deployment; this one demonstrates behaviour under scale and failure.

**D2 — SQS between S3 and the workers, rather than S3 → Lambda.**
Lambda would be simpler and genuinely cheaper for short jobs, but it caps out at 15
minutes, makes long transcodes awkward, and — more to the point — removes the queue
depth signal that the whole autoscaling story is built on.

**D3 — No LocalStack.**
Licensing changed; alternatives were not trustworthy. Test against real AWS.

**D4 — Output keys are `<job_id>/<resolution>.mp4`.**
Namespacing by job ID makes collisions impossible when two users upload `video1.mp4`,
groups every rendition of a job under one listable prefix, and makes each `output_keys`
URL reconstructible from two values already in the row — no mangled filenames stored
anywhere.

**D5 — Downscale only, based on a probe.**
Never produce a rendition larger than the source. Never trust the filename.

**D6 — Fail the whole job on any rendition failure.**
See [Failure handling](#failure-handling).

**D7 — Per-job work lives on a `worker` struct, not in the poll loop.**
`continue` inside a nested loop targets the inner loop, so abandoning a job from inside
the rendition loop needed either a flag, a labelled continue, or a function boundary.
The function boundary is the one that stays readable as the DB and delete steps land.

**D8 — Single-stage Dockerfile first; multi-stage later.**
Go compiles to a static binary, so the runtime image needs no toolchain — but ffmpeg is
a real OS-level program that must be installed by a package manager, and `scratch` has
neither a compiler nor CA root certificates for the binary's own TLS calls. Start on
Go+Alpine, shrink it as a separate, measurable optimisation.

**D9 — Multi-stage Dockerfile from the start, running as UID 10001.** _Supersedes D8._
A single stage ships the Go toolchain — a compiler and module cache an attacker who gets
code execution in the pod can use — to every node, for no runtime benefit. The build
stage is `golang:alpine`, the runtime stage plain `alpine` with only `ffmpeg` and CA
roots, both pinned by digest so a rebuild can't silently change base. The user is a
numeric UID so Kubernetes' `runAsNonRoot` can verify it without reading `/etc/passwd`.

**D10 — S3-native state locking, not DynamoDB.**
The roadmap said S3 + DynamoDB. Terraform 1.11 made locking with S3 conditional writes
(`use_lockfile`) generally available and deprecated the DynamoDB table, so the second
resource buys nothing but something else to provision, secure and pay for.

**D11 — No NAT gateway in phase 2.**
Nothing in the VPC needs outbound internet until the EKS nodes in phase 4, and an idle
NAT gateway is the largest fixed cost in the stack. Private route tables are already
per-AZ so phase 4 adds one route to each; S3 traffic uses the free gateway endpoint.

**D12 — Allowlist input containers; never let ffmpeg auto-detect.**
Uploads are attacker-controlled bytes fed to a large C parser. Accepting only mp4/mov and
mkv/webm, pinning the demuxer, and denying network protocols removes the playlist
formats that turn ffmpeg into a file reader or HTTP client. A legitimate upload in
another container fails loudly with `unsupported input format` and can be added
deliberately.

**D13 — `output_keys` stores keys, not URLs.**
There is no CloudFront distribution yet to build a URL from, and when there is, its
domain is configuration, not data: storing it would mean rewriting every row to move
domains. Per D4 the URL is `<distribution>/<key>`.

**D14 — Refuse unverified TLS to a remote database.**
pgx's default `sslmode=prefer` will talk plaintext to anything that answers on 5432. The
worker refuses to start against a non-local host without `verify-full`, and RDS has
`rds.force_ssl = 1`, so neither end can be talked into a downgrade.

---

## Well-Architected review

Filled in as each pillar earns an entry. The claim column stays empty until the
mechanism actually exists in the repository. "✅ written" means it exists as Terraform
that has been validated but not yet applied.

| Pillar                 | Mechanism                                                                       | Status      |
| ---------------------- | ------------------------------------------------------------------------------- | ----------- |
| Operational excellence | Structured logs keyed by job ID and object key                                  | partial     |
|                        | Prometheus/Grafana dashboard, stuck-job alert                                   | phase 7     |
|                        | GitOps delivery via ArgoCD                                                      | phase 6     |
| Security               | IRSA for pod-level AWS access, no static credentials                            | phase 4     |
|                        | Least-privilege worker IAM policy                                               | ✅ written  |
|                        | Kyverno admission policies; Trivy image scanning in CI                          | phases 6, 8 |
|                        | Secrets via SSM Parameter Store / Secrets Manager                               | phase 8     |
|                        | RDS master password managed by Secrets Manager                                  | ✅ written  |
|                        | Separate input and output buckets, enforced at start                            | ✅ done     |
|                        | Encryption at rest and TLS-only access for S3/SQS/RDS                           | ✅ written  |
|                        | Untrusted-upload hardening (format allowlist, no network from ffmpeg, size cap) | ✅ done     |
|                        | Non-root, digest-pinned, multi-stage image                                      | ✅ done     |
| Reliability            | SQS at-least-once delivery, visibility timeout, DLQ                             | ✅ done     |
|                        | Multi-AZ VPC and Multi-AZ RDS                                                   | ✅ written  |
|                        | Documented chaos test with measured MTTR                                        | phase 9     |
| Performance efficiency | Queue-depth autoscaling rather than CPU-based HPA                               | phase 5     |
|                        | Downscale-only transcoding                                                      | ✅ done     |
|                        | CloudFront in front of the output bucket                                        | phase 10    |
| Cost optimisation      | Scale to zero when the queue is empty                                           | phase 5     |
|                        | Spot instances for the worker node group                                        | phase 10    |
|                        | Measured cost per 1000 files                                                    | phase 10    |

---

## Roadmap

Immediate, in order:

1. ~~**`internal/db`** — connection setup, insert the job row on receipt (`pending`),
   transition through `processing` → `completed` / `failed`, write `output_keys`.~~
   Done; the row is inserted directly as `processing`, since the worker starts the job
   in the same breath.
2. ~~**Delete the SQS message** after the terminal status write, closing the at-least-once
   loop.~~ Done.
3. ~~**Dockerfile** — single stage on Go+Alpine with ffmpeg; multi-stage as a follow-up.~~
   Done, multi-stage from the start (D9).
4. ~~**Terraform (phase 2)** — VPC, bucket pair, queue plus DLQ and `maxReceiveCount`,
   RDS, with S3+DynamoDB remote state from the first commit.~~ Written and validated,
   with S3-native locking (D10); not yet applied.
5. **End-to-end run against real AWS**, replacing the manually created test resources
   that were torn down: apply both stacks, run the worker against the real queue and
   buckets, then destroy.
6. **Mark dead-lettered jobs** `dead_lettered`, and decide how redeliveries relate to
   rows (the idempotency open item).

---

## Maintaining this document

This README is structured so that finishing a phase means _adding_, not rewriting:

- Flip the row in [Build status](#build-status) and move items out of "what actually
  runs today".
- Append a new `D<n>` entry to [Design decisions](#design-decisions) for any choice that
  a reviewer would otherwise ask about. Never edit an old entry — supersede it.
- Fill in the [Well-Architected](#well-architected-review) row that the phase satisfies.
- Add a subsection under [The worker](#the-worker) — or a new top-level section for a
  new component — describing the mechanism and, more importantly, why it is that way.
- Strike completed items from the [Roadmap](#roadmap).

The sections that describe _reasoning_ are the ones worth the effort. Anyone can see
from the source that the queue is long-polled; the part worth writing down is why 20
seconds and what it costs if you don't.

---

## Notes

Region: `ap-south-1`. The AWS account ID and any bucket or queue ARNs are deliberately
kept out of this repository and supplied through environment variables and Terraform
variables.
