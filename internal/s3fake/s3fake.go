// Copyright 2026 Addison Huddy
// SPDX-License-Identifier: Apache-2.0

// Package s3fake is an in-process, in-memory stand-in for the S3 API, used by
// unit tests that exercise the blob store and registry without credentials or
// network access. It implements only the operations kfuse calls: PutObject,
// GetObject, DeleteObject and ListObjectsV2 (path-style addressing).
package s3fake

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Server is an HTTP server speaking enough of the S3 wire protocol for the
// kfuse call sites. Objects live in memory, keyed by "bucket/key".
type Server struct {
	http *httptest.Server

	mu      sync.Mutex
	objects map[string][]byte
	// FailPut, when set, makes every PutObject respond 500.
	failPut bool
	// Corrupt maps an object key to bytes served by GetObject instead of the
	// stored content (used to exercise checksum verification).
	corrupt map[string][]byte
	// beforePut runs (outside the lock) before a PutObject stores its body, so
	// tests can block an upload and interleave other work with it.
	beforePut func(bucket, key string, body []byte)
	// truncateGets counts GetObject responses still to be cut off mid-body.
	truncateGets int
}

// New starts a fake S3 server and registers cleanup on the test.
func New(t *testing.T) *Server {
	t.Helper()
	s := &Server{objects: map[string][]byte{}, corrupt: map[string][]byte{}}
	s.http = httptest.NewServer(s)
	t.Cleanup(s.http.Close)
	return s
}

// Client returns an S3 client wired to the fake server with path-style
// addressing (no DNS bucket subdomains).
func (s *Server) Client() *s3.Client {
	return s3.NewFromConfig(aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
	}, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(s.http.URL)
		o.UsePathStyle = true
	})
}

// BeforePut installs a hook invoked at the start of every PutObject, before
// the object is stored. Pass nil to remove it.
func (s *Server) BeforePut(hook func(bucket, key string, body []byte)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.beforePut = hook
}

// FailPuts makes subsequent PutObject calls fail with 500.
func (s *Server) FailPuts(fail bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failPut = fail
}

// TruncateGets makes the next n GetObject responses promise the whole object
// in Content-Length but close the connection after half the bytes, which is
// how a network blip mid-download surfaces to a streaming reader.
func (s *Server) TruncateGets(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.truncateGets = n
}

// Corrupt makes GetObject serve data for key instead of the stored content.
func (s *Server) Corrupt(bucket, key string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.corrupt[bucket+"/"+key] = data
}

// Put stores an object directly (test setup shortcut).
func (s *Server) Put(bucket, key string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[bucket+"/"+key] = data
}

// Get returns a stored object.
func (s *Server) Get(bucket, key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.objects[bucket+"/"+key]
	return data, ok
}

// Keys returns every stored key of a bucket, sorted.
func (s *Server) Keys(bucket string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for k := range s.objects {
		if b, key, ok := strings.Cut(k, "/"); ok && b == bucket {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	bucket, key, ok := splitPath(r.URL.Path)
	if !ok {
		writeError(w, http.StatusBadRequest, "InvalidRequest")
		return
	}
	switch r.Method {
	case http.MethodPut:
		s.put(w, r, bucket, key)
	case http.MethodGet:
		if key == "" || r.URL.Query().Get("list-type") == "2" {
			s.list(w, r, bucket)
			return
		}
		s.get(w, bucket, key)
	case http.MethodDelete:
		s.delete(w, bucket, key)
	default:
		writeError(w, http.StatusMethodNotAllowed, "MethodNotAllowed")
	}
}

func (s *Server) put(w http.ResponseWriter, r *http.Request, bucket, key string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "IncompleteBody")
		return
	}
	s.mu.Lock()
	hook := s.beforePut
	s.mu.Unlock()
	if hook != nil {
		hook(bucket, key, body)
	}
	s.mu.Lock()
	fail := s.failPut
	if !fail {
		s.objects[bucket+"/"+key] = body
	}
	s.mu.Unlock()
	if fail {
		writeError(w, http.StatusInternalServerError, "InternalError")
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) get(w http.ResponseWriter, bucket, key string) {
	s.mu.Lock()
	data, ok := s.objects[bucket+"/"+key]
	if bad, isBad := s.corrupt[bucket+"/"+key]; isBad {
		data, ok = bad, true
	}
	truncate := s.truncateGets > 0
	if truncate {
		s.truncateGets--
	}
	s.mu.Unlock()
	if !ok {
		writeError(w, http.StatusNotFound, "NoSuchKey")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprint(len(data)))
	w.WriteHeader(http.StatusOK)
	if truncate {
		// Flush so the client has the headers and a partial body in hand:
		// the failure then lands in the caller's body read, not in the
		// request the SDK retryer covers.
		_, _ = w.Write(data[:len(data)/2])
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		panic(http.ErrAbortHandler)
	}
	_, _ = w.Write(data)
}

func (s *Server) delete(w http.ResponseWriter, bucket, key string) {
	s.mu.Lock()
	delete(s.objects, bucket+"/"+key)
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

type listContents struct {
	Key  string `xml:"Key"`
	Size int64  `xml:"Size"`
}

type listResult struct {
	XMLName     xml.Name       `xml:"ListBucketResult"`
	Name        string         `xml:"Name"`
	Prefix      string         `xml:"Prefix"`
	KeyCount    int            `xml:"KeyCount"`
	IsTruncated bool           `xml:"IsTruncated"`
	Contents    []listContents `xml:"Contents"`
}

func (s *Server) list(w http.ResponseWriter, r *http.Request, bucket string) {
	prefix := r.URL.Query().Get("prefix")
	res := listResult{Name: bucket, Prefix: prefix}
	for _, key := range s.Keys(bucket) {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		data, _ := s.Get(bucket, key)
		res.Contents = append(res.Contents, listContents{Key: key, Size: int64(len(data))})
	}
	res.KeyCount = len(res.Contents)
	body, err := xml.Marshal(res)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "InternalError")
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// splitPath maps a path-style request path to bucket and key.
func splitPath(p string) (string, string, bool) {
	trimmed := strings.TrimPrefix(p, "/")
	if trimmed == "" {
		return "", "", false
	}
	bucket, key, _ := strings.Cut(trimmed, "/")
	return bucket, key, bucket != ""
}

func writeError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, "<Error><Code>%s</Code><Message>%s</Message></Error>", code, code)
}
