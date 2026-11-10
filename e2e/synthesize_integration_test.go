//go:build integration

// Package e2e runs the built image against RabbitMQ, Garage (S3) and WireMock (the client's callbacks).
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/garage"
	"github.com/testcontainers/testcontainers-go/modules/rabbitmq"
	tcnetwork "github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	garageImage   = "dxflrs/garage:v2.4.1"
	rabbitImage   = "rabbitmq:4.4-management-alpine"
	wiremockImage = "wiremock/wiremock:3.13.1"
	bucket        = "synthesize-e2e"
	accessKey     = "e2e-access-key"
	secretKey     = "e2e-secret-key-0123456789"
	garageAlias   = "garage"
	wiremockAlias = "wiremock"
	completeWait  = 60 * time.Second
)

type stack struct {
	backendURL  string
	wiremockURL string
	s3          *s3.Client
	presigner   *s3.PresignClient
}

type job struct {
	GenUploadURL      string `json:"genUploadUrl"`
	StatusCallbackURL string `json:"statusCallbackUrl"`
	objectName        string
}

type callback struct {
	JobID  string `json:"jobId"`
	Status string `json:"status"`
}

type wiremockRequests struct {
	Requests []struct {
		Request struct {
			Method string `json:"method"`
			URL    string `json:"url"`
			Body   string `json:"body"`
		} `json:"request"`
	} `json:"requests"`
}

func TestSynthesize(t *testing.T) {
	s := startStack(t)

	t.Run("calls its callbacks and uploads the MP3", func(t *testing.T) {
		j := s.prepareJob(t)
		jobID := s.submit(t, j, "Hello from the end-to-end test.")

		s.waitCompleted(t, j)

		_, err := uuid.Parse(jobID)
		require.NoError(t, err)
		callbacks := s.callbacks(t, j)
		statuses := make([]string, 0, len(callbacks))
		for _, c := range callbacks {
			assert.Equal(t, jobID, c.JobID)
			statuses = append(statuses, c.Status)
		}
		assert.Subset(t, statuses, []string{"queued", "generating", "uploading", "completed"})
		assert.Positive(t, s.objectSize(t, j))
	})

	t.Run("handles concurrent jobs", func(t *testing.T) {
		const jobs = 3
		var wg sync.WaitGroup
		prepared := make([]job, jobs)
		for i := range prepared {
			prepared[i] = s.prepareJob(t)
		}
		for i := range prepared {
			wg.Go(func() { s.submit(t, prepared[i], fmt.Sprintf("Concurrent synthesize load job %d.", i+1)) })
		}
		wg.Wait()

		for _, j := range prepared {
			s.waitCompleted(t, j)
			assert.Positive(t, s.objectSize(t, j))
		}
	})
}

func startStack(t *testing.T) *stack {
	t.Helper()
	ctx := t.Context()

	nw, err := tcnetwork.New(ctx)
	require.NoError(t, err)
	testcontainers.CleanupNetwork(t, nw)

	g, err := garage.Run(ctx, garageImage,
		garage.WithBucket(bucket),
		garage.WithCredentials(accessKey, secretKey),
		tcnetwork.WithNetwork([]string{garageAlias}, nw),
		testcontainers.WithWaitStrategy(wait.ForLog("S3 API server listening")),
	)
	testcontainers.CleanupContainer(t, g, testcontainers.StopTimeout(0))
	require.NoError(t, err)
	garageURL, err := g.ConnectionString(ctx)
	require.NoError(t, err)

	wm, err := testcontainers.Run(ctx, wiremockImage,
		testcontainers.WithExposedPorts("8080/tcp"),
		tcnetwork.WithNetwork([]string{wiremockAlias}, nw),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/__admin/mappings").WithPort("8080/tcp")),
	)
	testcontainers.CleanupContainer(t, wm)
	require.NoError(t, err)
	wiremockURL, err := wm.PortEndpoint(ctx, "8080/tcp", "http")
	require.NoError(t, err)

	rabbit, err := rabbitmq.Run(ctx, rabbitImage, tcnetwork.WithNetwork([]string{"rabbitmq"}, nw))
	testcontainers.CleanupContainer(t, rabbit)
	require.NoError(t, err)

	backend, err := testcontainers.Run(ctx, "",
		testcontainers.WithDockerfile(testcontainers.FromDockerfile{
			Context:    "..",
			Dockerfile: "Dockerfile",
			KeepImage:  true,
		}),
		testcontainers.WithEnv(map[string]string{
			"APP_ENV":                            "test",
			"RABBITMQ_URL":                       "amqp://guest:guest@rabbitmq:5672",
			"SYNTHESIZE_CALLBACK_ALLOWED_HOSTS":  garageAlias + "," + wiremockAlias,
			"SYNTHESIZE_REQUIRE_HTTPS_CALLBACKS": "false",
		}),
		testcontainers.WithExposedPorts("3000/tcp"),
		tcnetwork.WithNetwork([]string{"backend"}, nw),
		testcontainers.WithWaitStrategy(wait.ForHTTP("/health").WithPort("3000/tcp").WithStartupTimeout(120*time.Second)),
	)
	testcontainers.CleanupContainer(t, backend)
	require.NoError(t, err)
	backendURL, err := backend.PortEndpoint(ctx, "3000/tcp", "http")
	require.NoError(t, err)

	creds := credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")
	hostClient := s3.New(s3.Options{
		BaseEndpoint: aws.String(garageURL), Region: g.Region(), Credentials: creds, UsePathStyle: true,
	})
	// The backend resolves the presigned URL inside the Docker network, and the signature covers the host.
	internal := s3.New(s3.Options{
		BaseEndpoint: aws.String("http://" + garageAlias + ":3900"), Region: g.Region(), Credentials: creds, UsePathStyle: true,
	})

	return &stack{
		backendURL:  backendURL,
		wiremockURL: wiremockURL,
		s3:          hostClient,
		presigner:   s3.NewPresignClient(internal),
	}
}

// prepareJob stubs the client's two callbacks in WireMock. The upload-url callback returns a presigned PUT.
func (s *stack) prepareJob(t *testing.T) job {
	t.Helper()

	id := uuid.NewString()
	objectName := id + ".mp3"
	signed, err := s.presigner.PresignPutObject(t.Context(),
		&s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(objectName)},
		s3.WithPresignExpires(10*time.Minute),
	)
	require.NoError(t, err)

	s.stub(t, map[string]any{
		"request": map[string]any{"method": "POST", "urlPath": "/upload-url/" + id},
		"response": map[string]any{
			"status":   200,
			"headers":  map[string]string{"Content-Type": "application/json"},
			"jsonBody": map[string]string{"uploadUrl": signed.URL},
		},
	})
	s.stub(t, map[string]any{
		"request":  map[string]any{"method": "POST", "urlPath": "/status/" + id},
		"response": map[string]any{"status": 204},
	})

	return job{
		GenUploadURL:      "http://" + wiremockAlias + ":8080/upload-url/" + id,
		StatusCallbackURL: "http://" + wiremockAlias + ":8080/status/" + id,
		objectName:        objectName,
	}
}

func (s *stack) stub(t *testing.T, mapping map[string]any) {
	t.Helper()

	body, err := json.Marshal(mapping)
	require.NoError(t, err)
	resp := s.do(t, http.MethodPost, s.wiremockURL+"/__admin/mappings", body)
	defer resp.Body.Close() //nolint:errcheck // a test.
	require.Equal(t, http.StatusCreated, resp.StatusCode)
}

func (s *stack) submit(t *testing.T, j job, text string) string {
	t.Helper()

	body, err := json.Marshal(map[string]string{
		"text": text, "genUploadUrl": j.GenUploadURL, "statusCallbackUrl": j.StatusCallbackURL,
	})
	require.NoError(t, err)
	resp := s.do(t, http.MethodPost, s.backendURL+"/v1/synthesize", body)
	defer resp.Body.Close() //nolint:errcheck // a test.
	require.Equal(t, http.StatusAccepted, resp.StatusCode)

	var accepted struct {
		JobID string `json:"jobId"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&accepted))

	return accepted.JobID
}

func (s *stack) do(t *testing.T, method, target string, body []byte) *http.Response {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), method, target, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	return resp
}

func (s *stack) callbacks(t *testing.T, j job) []callback {
	t.Helper()

	statusURL, err := url.Parse(j.StatusCallbackURL)
	require.NoError(t, err)
	resp := s.do(t, http.MethodGet, s.wiremockURL+"/__admin/requests", nil)
	defer resp.Body.Close() //nolint:errcheck // a test.

	var all wiremockRequests
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&all))

	var out []callback
	for _, r := range all.Requests {
		if r.Request.Method != http.MethodPost || r.Request.URL != statusURL.Path {
			continue
		}
		var c callback
		require.NoError(t, json.Unmarshal([]byte(r.Request.Body), &c))
		out = append(out, c)
	}

	return out
}

func (s *stack) waitCompleted(t *testing.T, j job) {
	t.Helper()

	require.EventuallyWithT(t, func(c *assert.CollectT) {
		for _, cb := range s.callbacks(t, j) {
			if cb.Status == "completed" {
				return
			}
		}
		assert.Fail(c, "no completed callback yet")
	}, completeWait, 100*time.Millisecond)
}

func (s *stack) objectSize(t *testing.T, j job) int64 {
	t.Helper()

	head, err := s.s3.HeadObject(context.WithoutCancel(t.Context()),
		&s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(j.objectName)})
	require.NoError(t, err)

	return aws.ToInt64(head.ContentLength)
}
