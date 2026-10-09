package main

import (
	"strings"
	"testing"
)

func TestLoadSettings(t *testing.T) {
	valid := map[string]string{
		"QUEUE_URL":     "https://sqs.ap-south-1.amazonaws.com/000000000000/jobs",
		"INPUT_BUCKET":  "in",
		"OUTPUT_BUCKET": "out",
		"DATABASE_URL":  "postgres://localhost/db",
	}

	tests := []struct {
		name      string
		override  map[string]string
		wantErr   string
		wantLimit int64
	}{
		{name: "valid", wantLimit: defaultMaxInputBytes},
		{name: "custom limit", override: map[string]string{"MAX_INPUT_BYTES": "1048576"}, wantLimit: 1 << 20},
		{name: "missing values", override: map[string]string{"QUEUE_URL": "", "DATABASE_URL": ""}, wantErr: "QUEUE_URL, DATABASE_URL"},
		{name: "same bucket", override: map[string]string{"OUTPUT_BUCKET": "in"}, wantErr: "different buckets"},
		{name: "bad limit", override: map[string]string{"MAX_INPUT_BYTES": "lots"}, wantErr: "MAX_INPUT_BYTES"},
		{name: "zero limit", override: map[string]string{"MAX_INPUT_BYTES": "0"}, wantErr: "MAX_INPUT_BYTES"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range valid {
				t.Setenv(k, v)
			}
			t.Setenv("MAX_INPUT_BYTES", "")
			for k, v := range tt.override {
				t.Setenv(k, v)
			}

			s, err := loadSettings()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if s.MaxInputBytes != tt.wantLimit {
				t.Fatalf("MaxInputBytes = %d, want %d", s.MaxInputBytes, tt.wantLimit)
			}
		})
	}
}
