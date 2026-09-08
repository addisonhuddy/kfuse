// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package s3util

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	"github.com/addisonhuddy/kfuse/internal/config"
)

func TestNewClientConfiguresS3CompatibleEndpoint(t *testing.T) {
	client := NewClient(config.Config{
		S3Region:       "us-east-1",
		S3AccessKey:    "akid",
		S3SecretKey:    "secret",
		S3Endpoint:     "http://127.0.0.1:9000",
		S3UsePathStyle: true,
	})
	opts := client.Options()
	if got := aws.ToString(opts.BaseEndpoint); got != "http://127.0.0.1:9000" {
		t.Fatalf("BaseEndpoint = %q, want MinIO endpoint", got)
	}
	if !opts.UsePathStyle {
		t.Fatal("UsePathStyle must be enabled for a local S3 endpoint")
	}
	creds, err := opts.Credentials.Retrieve(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if creds.AccessKeyID != "akid" || creds.SecretAccessKey != "secret" {
		t.Fatal("static credentials were not configured")
	}
}

func TestNewClientUsesAWSEndpointByDefault(t *testing.T) {
	client := NewClient(config.Config{S3Region: "eu-west-2"})
	opts := client.Options()
	if opts.BaseEndpoint != nil {
		t.Fatalf("BaseEndpoint = %q, want AWS default resolution", aws.ToString(opts.BaseEndpoint))
	}
	if opts.UsePathStyle {
		t.Fatal("UsePathStyle must default to virtual-hosted addressing")
	}
	if opts.Region != "eu-west-2" {
		t.Fatalf("Region = %q, want eu-west-2", opts.Region)
	}
}
