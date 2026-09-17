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
the source object, probes its real height with `ffprobe`, transcodes it into every
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
| 2   | Core AWS plumbing — Terraform for VPC, S3, SQS, RDS, remote state               | ⬜ not started |
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
- ✅ `internal/queue` — long-polling receive, S3 event JSON parsed into a clean `Message`
- ✅ `internal/storage` — download from input bucket, upload to output bucket
- ✅ `internal/transcode` — `ffprobe` height detection, downscale-only rendition set
- ✅ `worker.processJob` — receive → download → transcode → upload, wired and compiling
- ⬜ `internal/db` — not started; no `jobs` row is written yet
- ⬜ SQS message deletion — not implemented, so every message currently redelivers forever
- ⬜ Dockerfile — reasoned through, not yet written
- ⬜ Any Terraform

---

## Repository layout

```
media-pipeline/
├── db/
│   └── migrations/
│       └── 0001_create_jobs_table.sql
├── local/
│   ├── docker-compose.yml          # Postgres for local development
│   ├── sqs-policy.json             # SQS resource policy allowing s3.amazonaws.com
│   └── s3-notification.json        # bucket notification config
├── worker/
│   ├── cmd/
│   │   └── worker/
│   │       └── main.go             # entrypoint: setup, poll loop, processJob
│   ├── internal/
│   │   ├── queue/                  # SQS receive + S3 event parsing
│   │   ├── storage/                # S3 download/upload
│   │   ├── transcode/              # ffprobe + ffmpeg
│   │   └── db/                     # (planned) job metadata persistence
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

### `internal/queue`

Receives one message at a time with a 20-second long poll — which is both cheaper than
short polling and the reason the loop doesn't spin at 100% CPU on an empty queue. The
raw S3 event JSON is unmarshalled into throwaway structs and reduced to a `Message`
carrying only what the rest of the system needs: the object key and the receipt handle.

The receipt handle is the claim ticket for deleting that specific delivery. SQS does not
delete on receive; if the message is never explicitly deleted it becomes visible again
when the visibility timeout expires. That property is the retry mechanism and the
reason idempotency matters.

### `internal/storage`

Wraps one S3 client and two bucket names — input and output are separate buckets, so
that a misconfigured worker cannot write derived files back into the source bucket and
retrigger itself. `Download` streams the object body to `/tmp` and returns the local
path; `Upload` streams a local file to a given key and returns only an error, since it
produces nothing the caller needs back.

### `internal/transcode`

Probes the source's real height with `ffprobe` before doing anything:

```
ffprobe -v error -select_streams v:0 -show_entries stream=height -of csv=p=0 <file>
```

Targets are `{1080, 720, 480, 360}`, stored as integers so the "is this target smaller
than the source" test is a numeric comparison. Anything larger than the source height is
skipped — upscaling burns CPU and storage to produce a file that is strictly worse than
the original. The result is a variable-length set of renditions, which falls out of that
filter rather than being special-cased.

Probing rather than trusting filenames is not paranoia: a file labelled "1080p" in
testing probed at 818 pixels tall, because it was a 2.35:1 cinematic crop.

---

## Data model

`db/migrations/0001_create_jobs_table.sql`

| column          | type            | notes                                                                         |
| --------------- | --------------- | ----------------------------------------------------------------------------- |
| `job_id`        | `TEXT` PK       | UUID generated by the worker                                                  |
| `status`        | `TEXT NOT NULL` | `CHECK` enum: `pending`, `processing`, `completed`, `failed`, `dead_lettered` |
| `input_key`     | `TEXT NOT NULL` | source object key                                                             |
| `output_keys`   | `JSONB`         | labelled `{resolution, url}` entries, null until complete                     |
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
rendition each URL is, and the set is variable-length by design.

---

## Configuration

All configuration is read from the environment — the same image runs locally, on a VM,
and in a Kubernetes Deployment, with values injected per environment. Nothing is
hardcoded and nothing is baked into the image.

| variable        | meaning                                                        |
| --------------- | -------------------------------------------------------------- |
| `QUEUE_URL`     | full SQS queue URL (the SQS API genuinely takes a URL)         |
| `INPUT_BUCKET`  | source bucket **name** — S3 addresses buckets by name, not URL |
| `OUTPUT_BUCKET` | destination bucket **name**                                    |
| `DATABASE_URL`  | _(planned)_ Postgres connection string                         |

AWS credentials are resolved through the default credential chain: environment
variables and shared config locally, IRSA in EKS. No static keys are ever mounted into
a pod.

---

## Running locally

Requirements: Go 1.27+, Docker, `ffmpeg` and `ffprobe` on `PATH`, AWS credentials with
access to an S3 bucket pair and an SQS queue.

```bash
# Postgres
cd local && docker compose up -d

# schema
psql "$DATABASE_URL" -f ../db/migrations/0001_create_jobs_table.sql

# worker
cd ../worker
go build ./...
QUEUE_URL=... INPUT_BUCKET=... OUTPUT_BUCKET=... go run ./cmd/worker
```

There is deliberately no LocalStack. As of March 2026 it requires an account and auth
token, and the no-account alternatives were not trustworthy enough to run against. The
S3 and SQS surface this project uses is small, so it is tested against real AWS.

---

## Failure handling

The contract is at-least-once delivery with the message deleted only after the job is
durably complete. Concretely:

| failure                    | behaviour                                                               |
| -------------------------- | ----------------------------------------------------------------------- |
| receive fails              | log, continue polling                                                   |
| download fails             | return error, message not deleted, redelivered after visibility timeout |
| `ffprobe` / `ffmpeg` fails | return error, message not deleted, redelivered                          |
| any upload fails           | return error, remaining renditions abandoned, message redelivered       |
| pod killed mid-job         | no delete happened, so the message reappears; row left in `processing`  |
| repeated failure           | `maxReceiveCount` exceeded → DLQ → job marked `dead_lettered`           |

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

---

## Well-Architected review

Filled in as each pillar earns an entry. The claim column stays empty until the
mechanism actually exists in the repository.

| Pillar                 | Mechanism                                              | Status      |
| ---------------------- | ------------------------------------------------------ | ----------- |
| Operational excellence | Structured logs keyed by job ID and object key         | partial     |
|                        | Prometheus/Grafana dashboard, stuck-job alert          | phase 7     |
|                        | GitOps delivery via ArgoCD                             | phase 6     |
| Security               | IRSA for pod-level AWS access, no static credentials   | phase 4     |
|                        | Kyverno admission policies; Trivy image scanning in CI | phases 6, 8 |
|                        | Secrets via SSM Parameter Store / Secrets Manager      | phase 8     |
|                        | Separate input and output buckets                      | ✅ done     |
| Reliability            | SQS at-least-once delivery, visibility timeout, DLQ    | partial     |
|                        | Multi-AZ VPC and Multi-AZ RDS                          | phase 2     |
|                        | Documented chaos test with measured MTTR               | phase 9     |
| Performance efficiency | Queue-depth autoscaling rather than CPU-based HPA      | phase 5     |
|                        | Downscale-only transcoding                             | ✅ done     |
|                        | CloudFront in front of the output bucket               | phase 10    |
| Cost optimisation      | Scale to zero when the queue is empty                  | phase 5     |
|                        | Spot instances for the worker node group               | phase 10    |
|                        | Measured cost per 1000 files                           | phase 10    |

---

## Roadmap

Immediate, in order:

1. **`internal/db`** — connection setup, insert the job row on receipt (`pending`),
   transition through `processing` → `completed` / `failed`, write `output_keys`.
2. **Delete the SQS message** after the terminal status write, closing the at-least-once
   loop.
3. **Dockerfile** — single stage on Go+Alpine with ffmpeg; multi-stage as a follow-up.
4. **Terraform (phase 2)** — VPC, bucket pair, queue plus DLQ and `maxReceiveCount`,
   RDS, with S3+DynamoDB remote state from the first commit.
5. **End-to-end run against real AWS**, replacing the manually created test resources
   that were torn down.

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
