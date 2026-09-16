// Package storage will contain a client struct with newclient constructor to wrap an s3 client
package storage

import (
	"context"
	"io"
	"os"
	"path/filepath"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type Client struct {
	S3Client     *s3.Client
	InputBucket  string
	OutputBucket string
}

func NewClient(s3Client *s3.Client, inputBucket string, outputBucket string) *Client {
	return &Client{
		S3Client:     s3Client,
		InputBucket:  inputBucket,
		OutputBucket: outputBucket,
	}
}

func (c *Client) Download(objectKey string) (string, error) {
	result, err := c.S3Client.GetObject(context.Background(), &s3.GetObjectInput{
		Bucket: aws.String(c.InputBucket),
		Key:    aws.String(objectKey),
	})
	if err != nil {
		return "", err
	}
	defer result.Body.Close()

	localPath := "/tmp/" + filepath.Base(objectKey)

	file, err := os.Create(localPath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	_, err = io.Copy(file, result.Body)
	if err != nil {
		return "", err
	}
	return localPath, nil
}

func (c *Client) Upload(localPath string, key string) error {
	file, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = c.S3Client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: aws.String(c.OutputBucket),
		Key:    aws.String(key),
		Body:   file,
	})
	if err != nil {
		return err
	}
	return nil
}
