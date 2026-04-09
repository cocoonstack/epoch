package server

import (
	"errors"
	"net/http/httptest"
	"testing"
)

func TestWriteUploadAppendError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode int
	}{
		{name: "not found", err: errUploadNotFound, wantCode: 404},
		{name: "too large", err: errUploadTooLarge, wantCode: 413},
		{name: "other", err: errors.New("boom"), wantCode: 500},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeUploadAppendError(w, tt.err)
			if w.Code != tt.wantCode {
				t.Errorf("status = %d, want %d", w.Code, tt.wantCode)
			}
		})
	}
}

func TestUploadLocation(t *testing.T) {
	got := uploadLocation("ubuntu", "abc-123")
	want := "/v2/ubuntu/blobs/uploads/abc-123"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
