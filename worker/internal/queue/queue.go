// Package queue contains a recieve function that needs to return s3 object key, reciet handle, and if the SQS queue is empty along with the error
package queue

import (
	"context"

	"encoding/json"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

//turning json field names into go data with using struct tags `` to map one to one

type S3Event struct {
	Records []S3EventRecord `json:"Records"`
}

type S3EventRecord struct {
	S3 S3Entity `json:"s3"`
}

type S3Entity struct {
	Object S3Object `json:"object"`
}

type S3Object struct {
	Key string `json:"key"`
}

type Message struct {
	ObjectKey     string
	ReceiptHandle string
}

type Client struct {
	SQSClient *sqs.Client
	QueueURL  string
}

func NewClient(sqsClient *sqs.Client, queueURL string) *Client {
	return &Client{
		SQSClient: sqsClient,
		QueueURL:  queueURL,
	}
}

func (c *Client) Receive() (Message, bool, error) {
	result, err := c.SQSClient.ReceiveMessage(context.Background(), &sqs.ReceiveMessageInput{
		QueueUrl:            aws.String(c.QueueURL),
		MaxNumberOfMessages: 1,
		WaitTimeSeconds:     20,
	})
	if err != nil {
		return Message{}, false, err
	}
	if len(result.Messages) == 0 {
		return Message{}, false, nil
	}
	var event S3Event
	err = json.Unmarshal([]byte(*result.Messages[0].Body), &event)
	if err != nil {
		return Message{}, false, err
	}

	msg := Message{
		ObjectKey:     event.Records[0].S3.Object.Key,
		ReceiptHandle: *result.Messages[0].ReceiptHandle,
	}
	return msg, true, nil
}
