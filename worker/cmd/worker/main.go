package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"

	"github.com/Naved124/media-pipeline-worker/internal/queue"
	"github.com/Naved124/media-pipeline-worker/internal/storage"
	"github.com/Naved124/media-pipeline-worker/internal/transcode"
)

func main() {
	// Our setup : create a SQS client
	cfg, err := config.LoadDefaultConfig(context.Background())
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

	for {
		// loop forever:
		//    receive one message from SQS -> get back: object key, receipt handle, found (yes/no), error
		// 		if message exists:
		// 				process it (fetch > transcode > upload > write statue > delete message)
		// 		(if no message, loop just continues)
		msg, found, err := queueClient.Receive()
		if err != nil {
			log.Printf("error recieveing message: %v", err)
			continue
		}
		if !found {
			continue
		}
		jobID := uuid.New().String()
		localPath, err := storageClient.Download(msg.ObjectKey)
		if err != nil {
			log.Printf("failed to download %s: %v", msg.ObjectKey, err)
			continue
		}
		log.Printf("path of the file: %s", localPath)

		files, err := transcode.Transcode(localPath)
		if err != nil {
			log.Printf("files not fetched: %s, %v", jobID, err)
			continue
		}
		for _, n := range files {
			log.Printf("filepath is : %s , resolution is : %s", n.FilePath, n.Resolution)
			key := fmt.Sprintf("%s/%s.mp4", jobID, n.Resolution)

			err := storageClient.Upload(n.FilePath, key)
			if err != nil {
				log.Printf("this job failed : %s, %v", jobID, err)
			}

		}

	}
}
