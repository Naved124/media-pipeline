package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

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

// failWriteTimeout bounds the failed-status write, which runs on a context
// detached from the (possibly already expired) job context.
const failWriteTimeout = 10 * time.Second

func main() {
	// Our setup : create a SQS client
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		log.Fatalf("failed to load AWS config: %v", err)
	}

	sqsClient := sqs.NewFromConfig(cfg)
	queueURL := os.Getenv("QUEUE_URL")

	queueClient := queue.NewClient(sqsClient, queueURL)

	//create a s3 client
	s3Client := s3.NewFromConfig(cfg)
	inputBucket := os.Getenv("INPUT_BUCKET")
	outputBucket := os.Getenv("OUTPUT_BUCKET")

	storageClient := storage.NewClient(s3Client, inputBucket, outputBucket)

	// create the postgres pool, once, and check it is reachable before polling
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatalf("failed to create database pool: %v", err)
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		log.Fatalf("failed to reach database: %v", err)
	}
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

		jobCtx, jobCancel := context.WithTimeout(context.Background(), 5*time.Minute+30*time.Second)

		err = w.processJob(jobCtx, msg)
		jobCancel()
		if err != nil {
			log.Printf("job failed: %v", err)
			continue
		}
	}
}

func (w *worker) processJob(ctx context.Context, msg queue.Message) error {
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
	localPath, err := w.StorageClient.Download(ctx, objectKey)
	if err != nil {
		return nil, fmt.Errorf("failed to download %s: %w", objectKey, err)
	}
	log.Printf("path of the file: %s", localPath)

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
