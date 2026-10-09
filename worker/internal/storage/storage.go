// Package storage will contain a client struct with newclient constructor to wrap an s3 client
package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// sourceName is the fixed local name of every download. Nothing derived from
// the object key touches the filesystem.
const sourceName = "source"

type Client struct {
	S3Client     *s3.Client
	InputBucket  string
	OutputBucket string
	// MaxInputBytes caps a download so one upload can't fill the disk.
	MaxInputBytes int64
}

func NewClient(s3Client *s3.Client, inputBucket string, outputBucket string, maxInputBytes int64) *Client {
	return &Client{
		S3Client:      s3Client,
		InputBucket:   inputBucket,
		OutputBucket:  outputBucket,
		MaxInputBytes: maxInputBytes,
	}
}

// Download streams objectKey from the input bucket into dir, which the caller
// owns and removes, and returns the local path.
func (c *Client) Download(ctx context.Context, objectKey string, dir string) (string, error) {
	result, err := c.S3Client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.InputBucket),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		return "", err
	}
	defer result.Body.Close()

	if size := aws.ToInt64(result.ContentLength); size > c.MaxInputBytes {
		return "", fmt.Errorf("object is %d bytes, limit is %d", size, c.MaxInputBytes)
	}

	localPath := filepath.Join(dir, sourceName)

	// O_EXCL: never follow or overwrite something already at this path
	file, err := os.OpenFile(localPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) // #nosec G304 -- fixed name inside the caller's private temp dir
	if err != nil {
		return "", err
	}

	// read one byte past the limit so a body longer than its ContentLength is caught
	n, err := io.Copy(file, io.LimitReader(result.Body, c.MaxInputBytes+1))
	if err != nil {
		_ = file.Close() // the copy error is the one worth returning
		return "", err
	}
	if n > c.MaxInputBytes {
		_ = file.Close()
		return "", fmt.Errorf("object exceeds %d bytes", c.MaxInputBytes)
	}
	// a failed close can mean a failed write, so it is checked, not deferred
	if err := file.Close(); err != nil {
		return "", err
	}
	return localPath, nil
}

func (c *Client) Upload(ctx context.Context, localPath string, key string) error {
	file, err := os.Open(localPath) // #nosec G304 -- localPath is a rendition inside the job's own temp dir
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = c.S3Client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(c.OutputBucket),
		Key:    aws.String(key),
		Body:   file,
	})
	if err != nil {
		return err
	}
	return nil
}
