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

	"github.com/Naved124/media-pipeline-worker/internal/queue"
	"github.com/Naved124/media-pipeline-worker/internal/storage"
	"github.com/Naved124/media-pipeline-worker/internal/transcode"
)

type worker struct {
	QueueClient   *queue.Client
	StorageClient *storage.Client
}

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

	w := &worker{
		QueueClient:   queueClient,
		StorageClient: storageClient,
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
	localPath, err := w.StorageClient.Download(ctx, msg.ObjectKey)
	if err != nil {
		return fmt.Errorf("failed to download %s: %w", msg.ObjectKey, err)
	}
	log.Printf("path of the file: %s", localPath)

	files, err := transcode.Transcode(localPath)
	if err != nil {
		return fmt.Errorf("files not fetched: %s, %w", jobID, err)
	}

	for _, n := range files {
		log.Printf("filepath is : %s , resolution is : %s", n.FilePath, n.Resolution)
		key := fmt.Sprintf("%s/%s.mp4", jobID, n.Resolution)

		err := w.StorageClient.Upload(ctx, n.FilePath, key)
		if err != nil {
			return fmt.Errorf("this job failed : %s, %s, %w", jobID, key, err)
		}

	}

	return nil

}
