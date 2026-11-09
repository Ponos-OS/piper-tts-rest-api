package synthesis_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kasir-barati/piper-tts-rest-api/internal/synthesis"
)

const jobID = "e52cbe80-17de-4204-a983-bb1c10c36341"

func TestStatusPayload_JSONHasOnlyTheFieldsOfItsStatus(t *testing.T) {
	failedAt := time.Date(2026, 8, 19, 12, 0, 0, 0, time.FixedZone("CEST", 2*60*60))
	tests := []struct {
		name    string
		payload synthesis.StatusPayload
		want    string
	}{
		{"queued", synthesis.Queued(jobID, 1), `{"jobId":"` + jobID + `","status":"queued","attempt":1}`},
		{"generating", synthesis.Generating(jobID, 2, 40), `{"jobId":"` + jobID + `","status":"generating","attempt":2,"percent":40}`},
		{"generating at zero", synthesis.Generating(jobID, 1, 0), `{"jobId":"` + jobID + `","status":"generating","attempt":1,"percent":0}`},
		{"uploading", synthesis.Uploading(jobID, 1, 99), `{"jobId":"` + jobID + `","status":"uploading","attempt":1,"percent":99}`},
		{"completed", synthesis.Completed(jobID, 1, 123456), `{"jobId":"` + jobID + `","status":"completed","attempt":1,"fileSizeBytes":123456}`},
		{
			"failed",
			synthesis.Failed(jobID, 3, failedAt, synthesis.JobError{Code: synthesis.CodeUploadFailed, Message: "MP3 upload failed"}),
			`{"jobId":"` + jobID + `","status":"failed","attempt":3,"failedAt":"2026-08-19T10:00:00Z","error":{"code":"upload-failed","message":"MP3 upload failed"}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(tt.payload)

			require.NoError(t, err)
			assert.JSONEq(t, tt.want, string(got))
		})
	}
}

func TestProgress_IsClampedTo0Through99(t *testing.T) {
	assert.Equal(t, 0, *synthesis.Generating(jobID, 1, -5).Percent)
	assert.Equal(t, 99, *synthesis.Generating(jobID, 1, 100).Percent)
	assert.Equal(t, 99, *synthesis.Uploading(jobID, 1, 250).Percent)
}
