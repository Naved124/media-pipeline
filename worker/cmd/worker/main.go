package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"

	"github.com/Naved124/media-pipeline-worker/internal/db"
	"github.com/Naved124/media-pipeline-worker/internal/queue"
	"github.com/Naved124/media-pipeline-worker/internal/storage"
	"github.com/Naved124/media-pipeline-worker/internal/transcode"
)

type worker struct {
	QueueClient   *queue.Client
	StorageClient *storage.Client
	DBClient      *db.Client
}

const (
	// jobTimeout must stay below the queue's visibility timeout (360s in
	// terraform/pipeline), or a slow job is redelivered while still running.
	jobTimeout = 5*time.Minute + 30*time.Second

	// failWriteTimeout bounds the failed-status write, which runs on a context
	// detached from the (possibly already expired) job context.
	failWriteTimeout = 10 * time.Second

	defaultMaxInputBytes = 2 << 30 // 2 GiB
)

// settings is everything the worker reads from its environment.
type settings struct {
	QueueURL      string
	InputBucket   string
	OutputBucket  string
	DatabaseURL   string
	MaxInputBytes int64
}

// loadSettings validates the environment up front, so a misconfigured pod
// fails at start rather than on its first message.
func loadSettings() (settings, error) {
	s := settings{
		QueueURL:      os.Getenv("QUEUE_URL"),
		InputBucket:   os.Getenv("INPUT_BUCKET"),
		OutputBucket:  os.Getenv("OUTPUT_BUCKET"),
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		MaxInputBytes: defaultMaxInputBytes,
	}

	var missing []string
	for _, v := range []struct{ name, value string }{
		{"QUEUE_URL", s.QueueURL},
		{"INPUT_BUCKET", s.InputBucket},
		{"OUTPUT_BUCKET", s.OutputBucket},
		{"DATABASE_URL", s.DatabaseURL},
	} {
		if v.value == "" {
			missing = append(missing, v.name)
		}
	}
	if len(missing) > 0 {
		return s, fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}

	// the separation is what stops renditions retriggering the worker
	if s.InputBucket == s.OutputBucket {
		return s, fmt.Errorf("INPUT_BUCKET and OUTPUT_BUCKET must be different buckets")
	}

	if raw := os.Getenv("MAX_INPUT_BYTES"); raw != "" {
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || n <= 0 {
			return s, fmt.Errorf("MAX_INPUT_BYTES must be a positive integer, got %q", raw)
		}
		s.MaxInputBytes = n
	}
	return s, nil
}

func main() {
	// Our setup : create a SQS client
	env, err := loadSettings()
	if err != nil {
		log.Fatalf("invalid configuration: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		log.Fatalf("failed to load AWS config: %v", err)
	}

	sqsClient := sqs.NewFromConfig(cfg)

	queueClient := queue.NewClient(sqsClient, env.QueueURL)

	//create a s3 client
	s3Client := s3.NewFromConfig(cfg)

	storageClient := storage.NewClient(s3Client, env.InputBucket, env.OutputBucket, env.MaxInputBytes)

	// create the postgres pool, once, and check it is reachable before polling
	pool, err := db.Connect(ctx, env.DatabaseURL)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer pool.Close()
	dbClient := db.NewClient(pool)

	w := &worker{
		QueueClient:   queueClient,
		StorageClient: storageClient,
		DBClient:      dbClient,
	}

	for {
		// loop forever:
		//    receive one message from SQS -> get back: object key, receipt handle, found (yes/no), error
		// 		if message exists:
		// 				process it (fetch > transcode > upload > write statue > delete message)
		// 		(if no message, loop just continues)
		if ctx.Err() != nil {
			log.Println("the worker is shutting down")
			break
		}
		msg, found, err := queueClient.Receive(ctx)
		if err != nil {
			log.Printf("error recieveing message: %v", err)
			continue
		}
		if !found {
			continue
		}

		jobCtx, jobCancel := context.WithTimeout(context.Background(), jobTimeout)

		err = w.processJob(jobCtx, msg)
		jobCancel()
		if err != nil {
			log.Printf("job failed: %v", err)
			continue
		}
	}
}

func (w *worker) processJob(ctx context.Context, msg queue.Message) error {
	// Download always reads the configured input bucket, so an event naming
	// any other bucket would process the wrong object; leave it for the DLQ
	if msg.Bucket != w.StorageClient.InputBucket {
		return fmt.Errorf("event for bucket %q, expected %q", msg.Bucket, w.StorageClient.InputBucket)
	}

	jobID := uuid.New().String()

	// the row is written on receipt, so a pod killed mid-job leaves a
	// queryable 'processing' row behind
	if err := w.DBClient.CreateJob(ctx, jobID, msg.ObjectKey); err != nil {
		return err
	}

	outputs, err := w.transcodeAndUpload(ctx, jobID, msg.ObjectKey)
	if err != nil {
		// the job context may be the thing that failed, so record the
		// failure on a fresh one; the message is not deleted and redelivers
		failCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), failWriteTimeout)
		defer cancel()
		if dbErr := w.DBClient.FailJob(failCtx, jobID, err.Error()); dbErr != nil {
			log.Printf("job %s: %v", jobID, dbErr)
		}
		return err
	}

	if err := w.DBClient.CompleteJob(ctx, jobID, outputs); err != nil {
		return err
	}

	// only delete once the job is durably complete
	if err := w.QueueClient.Delete(ctx, msg.ReceiptHandle); err != nil {
		return fmt.Errorf("job %s completed but message not deleted: %w", jobID, err)
	}
	log.Printf("job %s completed: %d renditions", jobID, len(outputs))
	return nil
}

func (w *worker) transcodeAndUpload(ctx context.Context, jobID string, objectKey string) ([]db.Output, error) {
	// one private (0700) directory per job, removed when the job ends however
	// it ends, so files can't collide across jobs or accumulate on the node
	jobDir, err := os.MkdirTemp("", "job-"+jobID+"-")
	if err != nil {
		return nil, fmt.Errorf("creating work directory for job %s: %w", jobID, err)
	}
	defer func() {
		if err := os.RemoveAll(jobDir); err != nil {
			log.Printf("job %s: removing work directory: %v", jobID, err)
		}
	}()

	localPath, err := w.StorageClient.Download(ctx, objectKey, jobDir)
	if err != nil {
		return nil, fmt.Errorf("failed to download %q: %w", objectKey, err)
	}
	log.Printf("job %s: downloaded %q to %s", jobID, objectKey, localPath)

	files, err := transcode.Transcode(ctx, localPath)
	if err != nil {
		return nil, fmt.Errorf("files not fetched: %s, %w", jobID, err)
	}

	outputs := make([]db.Output, 0, len(files))
	for _, n := range files {
		log.Printf("filepath is : %s , resolution is : %s", n.FilePath, n.Resolution)
		key := fmt.Sprintf("%s/%s.mp4", jobID, n.Resolution)

		err := w.StorageClient.Upload(ctx, n.FilePath, key)
		if err != nil {
			return nil, fmt.Errorf("this job failed : %s, %s, %w", jobID, key, err)
		}
		outputs = append(outputs, db.Output{Resolution: n.Resolution, Key: key})
	}

	return outputs, nil
}
