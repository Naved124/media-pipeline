// Package queue contains a recieve function that needs to return s3 object key, reciet handle, and if the SQS queue is empty along with the error
package queue

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"encoding/json"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
)

//turning json field names into go data with using struct tags `` to map one to one

type S3Event struct {
	Records []S3EventRecord `json:"Records"`
	// Event is only set on the s3:TestEvent S3 sends when a notification is configured.
	Event string `json:"Event"`
}

type S3EventRecord struct {
	EventSource string   `json:"eventSource"`
	EventName   string   `json:"eventName"`
	S3          S3Entity `json:"s3"`
}

type S3Entity struct {
	Bucket S3Bucket `json:"bucket"`
	Object S3Object `json:"object"`
}

type S3Bucket struct {
	Name string `json:"name"`
}

type S3Object struct {
	Key string `json:"key"`
}

type Message struct {
	Bucket        string
	ObjectKey     string
	ReceiptHandle string
}

// errTestEvent marks the s3:TestEvent S3 publishes when a bucket notification
// is created. It is not a job.
var errTestEvent = errors.New("s3 test event")

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

func (c *Client) Receive(ctx context.Context) (Message, bool, error) {
	result, err := c.SQSClient.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
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
	raw := result.Messages[0]
	if raw.ReceiptHandle == nil {
		return Message{}, false, errors.New("message has no receipt handle")
	}

	msg, err := parseEvent(aws.ToString(raw.Body))
	if errors.Is(err, errTestEvent) {
		if err := c.Delete(ctx, *raw.ReceiptHandle); err != nil {
			return Message{}, false, fmt.Errorf("deleting s3 test event: %w", err)
		}
		return Message{}, false, nil
	}
	if err != nil {
		// not deleted: it redelivers until maxReceiveCount moves it to the DLQ
		return Message{}, false, fmt.Errorf("message %s: %w", aws.ToString(raw.MessageId), err)
	}

	msg.ReceiptHandle = *raw.ReceiptHandle
	return msg, true, nil
}

// parseEvent reduces an S3 event notification body to a Message. The body is
// untrusted input, so anything that isn't exactly one ObjectCreated record
// from S3 is rejected rather than guessed at.
func parseEvent(body string) (Message, error) {
	var event S3Event
	if err := json.Unmarshal([]byte(body), &event); err != nil {
		return Message{}, fmt.Errorf("not an S3 event: %w", err)
	}
	if event.Event == "s3:TestEvent" {
		return Message{}, errTestEvent
	}
	// S3 always sends one record per notification
	if len(event.Records) != 1 {
		return Message{}, fmt.Errorf("expected 1 record, got %d", len(event.Records))
	}

	record := event.Records[0]
	if record.EventSource != "aws:s3" {
		return Message{}, fmt.Errorf("unexpected event source %q", record.EventSource)
	}
	if !strings.HasPrefix(record.EventName, "ObjectCreated:") {
		return Message{}, fmt.Errorf("unexpected event name %q", record.EventName)
	}
	if record.S3.Bucket.Name == "" {
		return Message{}, errors.New("event has no bucket name")
	}

	// keys arrive URL-encoded, with spaces as '+'
	key, err := url.QueryUnescape(record.S3.Object.Key)
	if err != nil {
		return Message{}, fmt.Errorf("decoding object key %q: %w", record.S3.Object.Key, err)
	}
	// postgres TEXT rejects NUL and invalid UTF-8, so catch them here rather
	// than as a confusing insert failure
	if key == "" || !utf8.ValidString(key) || strings.ContainsRune(key, 0) {
		return Message{}, fmt.Errorf("invalid object key %q", key)
	}

	return Message{Bucket: record.S3.Bucket.Name, ObjectKey: key}, nil
}

// Delete removes one delivery from the queue. Until it is called, the message
// becomes visible again when its visibility timeout expires.
func (c *Client) Delete(ctx context.Context, receiptHandle string) error {
	_, err := c.SQSClient.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      aws.String(c.QueueURL),
		ReceiptHandle: aws.String(receiptHandle),
	})
	return err
}
