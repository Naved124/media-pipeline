package queue

import (
	"errors"
	"fmt"
	"testing"
)

// s3Event builds a notification body in the shape S3 sends to SQS.
func s3Event(source, name, bucket, key string) string {
	return fmt.Sprintf(`{"Records":[{"eventVersion":"2.1","eventSource":%q,"awsRegion":"ap-south-1",
		"eventName":%q,"s3":{"bucket":{"name":%q,"arn":"arn:aws:s3:::%s"},
		"object":{"key":%q,"size":1024}}}]}`, source, name, bucket, bucket, key)
}

func TestParseEvent(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantKey    string
		wantBucket string
		wantErr    bool
	}{
		{
			name:       "plain key",
			body:       s3Event("aws:s3", "ObjectCreated:Put", "in", "videos/clip.mp4"),
			wantKey:    "videos/clip.mp4",
			wantBucket: "in",
		},
		{
			name:       "url-encoded key with spaces and unicode",
			body:       s3Event("aws:s3", "ObjectCreated:CompleteMultipartUpload", "in", "my+test+video+%C3%A9.mp4"),
			wantKey:    "my test video é.mp4",
			wantBucket: "in",
		},
		{
			name:       "literal plus is percent-encoded by S3",
			body:       s3Event("aws:s3", "ObjectCreated:Put", "in", "a%2Bb.mp4"),
			wantKey:    "a+b.mp4",
			wantBucket: "in",
		},
		{name: "not json", body: "hello", wantErr: true},
		{name: "empty object", body: `{}`, wantErr: true},
		{name: "empty records", body: `{"Records":[]}`, wantErr: true},
		{
			name:    "two records",
			body:    `{"Records":[{"eventSource":"aws:s3","eventName":"ObjectCreated:Put","s3":{"bucket":{"name":"in"},"object":{"key":"a"}}},{"eventSource":"aws:s3","eventName":"ObjectCreated:Put","s3":{"bucket":{"name":"in"},"object":{"key":"b"}}}]}`,
			wantErr: true,
		},
		{name: "wrong source", body: s3Event("aws:sns", "ObjectCreated:Put", "in", "a.mp4"), wantErr: true},
		{name: "delete event", body: s3Event("aws:s3", "ObjectRemoved:Delete", "in", "a.mp4"), wantErr: true},
		{name: "missing bucket", body: s3Event("aws:s3", "ObjectCreated:Put", "", "a.mp4"), wantErr: true},
		{name: "empty key", body: s3Event("aws:s3", "ObjectCreated:Put", "in", ""), wantErr: true},
		{name: "bad percent-encoding", body: s3Event("aws:s3", "ObjectCreated:Put", "in", "a%zz.mp4"), wantErr: true},
		{name: "NUL in key", body: s3Event("aws:s3", "ObjectCreated:Put", "in", "a%00.mp4"), wantErr: true},
		{name: "invalid UTF-8 in key", body: s3Event("aws:s3", "ObjectCreated:Put", "in", "a%FF.mp4"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg, err := parseEvent(tt.body)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got %+v", msg)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if msg.ObjectKey != tt.wantKey || msg.Bucket != tt.wantBucket {
				t.Fatalf("got bucket %q key %q, want bucket %q key %q", msg.Bucket, msg.ObjectKey, tt.wantBucket, tt.wantKey)
			}
		})
	}
}

// The body S3 publishes when a notification configuration is saved. Before
// parseEvent existed, this indexed Records[0] on an empty slice and panicked.
func TestParseEventTestEvent(t *testing.T) {
	body := `{"Service":"Amazon S3","Event":"s3:TestEvent","Time":"2026-10-09T10:00:00.000Z",
		"Bucket":"in","RequestId":"ABC","HostId":"XYZ"}`
	if _, err := parseEvent(body); !errors.Is(err, errTestEvent) {
		t.Fatalf("expected errTestEvent, got %v", err)
	}
}
