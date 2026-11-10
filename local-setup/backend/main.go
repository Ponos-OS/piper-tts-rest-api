// Command backend is a stand-in for a client of POST /v1/synthesize, for local testing.
// It answers the genUploadUrl callback with a presigned PUT URL for Garage and logs the status callbacks.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const (
	urlTTL            = 15 * time.Minute
	readHeaderTimeout = 10 * time.Second
	statusCompleted   = "completed"
)

type backend struct {
	bucket string
	upload *s3.PresignClient // signs for the address the worker uses inside the Docker network.
	public *s3.PresignClient // signs for the address your machine uses.
}

type uploadURLRequest struct {
	JobID       string `json:"jobId"`
	Attempt     int    `json:"attempt"`
	ContentType string `json:"contentType"`
}

func main() {
	region := env("S3_REGION", "garage")
	creds := credentials.NewStaticCredentialsProvider(env("S3_ACCESS_KEY", "piper-access-key"), env("S3_SECRET_KEY", "piper-secret-key-0123456789"), "")
	client := func(endpoint string) *s3.PresignClient {
		return s3.NewPresignClient(s3.New(s3.Options{BaseEndpoint: aws.String(endpoint), Region: region, Credentials: creds, UsePathStyle: true}))
	}

	b := &backend{
		bucket: env("S3_BUCKET", "piper"),
		upload: client(env("S3_ENDPOINT", "http://garage:3900")),
		public: client(env("S3_PUBLIC_ENDPOINT", "http://localhost:3900")),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /upload-url", b.handleUploadURL)
	mux.HandleFunc("POST /status", b.handleStatus)

	srv := &http.Server{Addr: ":8080", Handler: mux, ReadHeaderTimeout: readHeaderTimeout}
	log.Fatal(srv.ListenAndServe())
}

func (b *backend) handleUploadURL(w http.ResponseWriter, r *http.Request) {
	var req uploadURLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}

	signed, err := b.upload.PresignPutObject(r.Context(), &s3.PutObjectInput{
		Bucket:      aws.String(b.bucket),
		Key:         aws.String(req.JobID + ".mp3"),
		ContentType: aws.String(req.ContentType),
	}, s3.WithPresignExpires(urlTTL))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)

		return
	}

	log.Printf("upload-url job=%s attempt=%d", req.JobID, req.Attempt)
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]string{"uploadUrl": signed.URL}); err != nil {
		log.Printf("reply: %v", err)
	}
}

func (b *backend) handleStatus(w http.ResponseWriter, r *http.Request) {
	var report map[string]any
	if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)

		return
	}

	log.Printf("status key=%s %v", r.Header.Get("Idempotency-Key"), report) //nolint:gosec // a local test tool logging what it receives.
	if report["status"] == statusCompleted {
		b.logDownloadURL(r.Context(), r.Header.Get("Idempotency-Key"))
	}
	w.WriteHeader(http.StatusNoContent)
}

// logDownloadURL prints a link to the uploaded MP3. The key is "<jobId>:<attempt>".
func (b *backend) logDownloadURL(ctx context.Context, idempotencyKey string) {
	var jobID string
	if _, err := fmt.Sscanf(idempotencyKey, "%36s", &jobID); err != nil {
		return
	}

	signed, err := b.public.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(b.bucket), Key: aws.String(jobID + ".mp3")}, s3.WithPresignExpires(urlTTL))
	if err != nil {
		log.Printf("download link: %v", err)

		return
	}
	log.Printf("download: %s", signed.URL)
}

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}

	return fallback
}
