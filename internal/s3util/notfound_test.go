// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

package s3util

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

func TestIsNotFound(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"no such key", &types.NoSuchKey{}, true},
		{"wrapped no such key", fmt.Errorf("registry: get k: %w", &types.NoSuchKey{}), true},
		{"head not found", &types.NotFound{}, true},
		{"api error code", &smithy.GenericAPIError{Code: "NoSuchKey"}, true},
		{"access denied", &smithy.GenericAPIError{Code: "AccessDenied", Message: "object not found in policy"}, false},
		{"message mentions 404", errors.New("dial tcp: request 404abc failed"), false},
		{"message mentions not found", errors.New("host not found"), false},
		{"other api error", &smithy.GenericAPIError{Code: "SlowDown"}, false},
		{"no such bucket", &types.NoSuchBucket{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsNotFound(tc.err); got != tc.want {
				t.Fatalf("IsNotFound(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// S3 answers NoSuchBucket with a 404 too, but a misconfigured bucket must not
// read as "object absent": that turns an unreadable lease into an unheld one.
func TestIsNotFoundIgnoresNoSuchBucket404(t *testing.T) {
	err := &awshttp.ResponseError{
		ResponseError: &smithyhttp.ResponseError{
			Response: &smithyhttp.Response{Response: &http.Response{StatusCode: http.StatusNotFound}},
			Err:      &types.NoSuchBucket{},
		},
	}
	if IsNotFound(fmt.Errorf("registry: get k: %w", err)) {
		t.Fatal("NoSuchBucket must not be classified as an absent object")
	}
}

func TestIsNotFoundHTTPStatus(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   bool
	}{{http.StatusNotFound, true}, {http.StatusForbidden, false}, {http.StatusInternalServerError, false}} {
		err := &awshttp.ResponseError{
			ResponseError: &smithyhttp.ResponseError{
				Response: &smithyhttp.Response{Response: &http.Response{StatusCode: tc.status}},
				Err:      errors.New("boom"),
			},
		}
		if got := IsNotFound(fmt.Errorf("registry: get k: %w", err)); got != tc.want {
			t.Fatalf("status %d: IsNotFound = %v, want %v", tc.status, got, tc.want)
		}
	}
}
