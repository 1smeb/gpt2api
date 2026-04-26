package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestOpenAIImageGenerateWithReferencesUsesImageEditsMultipart(t *testing.T) {
	imgA := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 'a'}
	imgB := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 'b'}

	a := &openaiAdapter{
		baseURL: "https://upstream.test",
		apiKey:  "test-key",
		client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method != http.MethodPost {
				return nil, fmt.Errorf("method = %s", r.Method)
			}
			if r.URL.Path != "/v1/images/edits" {
				return nil, fmt.Errorf("path = %s", r.URL.Path)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
				return nil, fmt.Errorf("authorization = %q", got)
			}
			if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
				return nil, fmt.Errorf("content-type = %q", r.Header.Get("Content-Type"))
			}
			if err := r.ParseMultipartForm(10 << 20); err != nil {
				return nil, err
			}
			for k, want := range map[string]string{
				"model":  "gpt-image-1",
				"prompt": "make it brighter",
				"n":      "2",
				"size":   "1024x1024",
			} {
				if got := r.FormValue(k); got != want {
					return nil, fmt.Errorf("%s = %q, want %q", k, got, want)
				}
			}
			files := r.MultipartForm.File["image"]
			if len(files) != 2 {
				return nil, fmt.Errorf("image files = %d, want 2", len(files))
			}
			for i, fh := range files {
				f, err := fh.Open()
				if err != nil {
					return nil, err
				}
				data, err := io.ReadAll(f)
				_ = f.Close()
				if err != nil {
					return nil, err
				}
				want := [][]byte{imgA, imgB}[i]
				if string(data) != string(want) {
					return nil, fmt.Errorf("image[%d] bytes = %q, want %q", i, data, want)
				}
			}

			return jsonResponse(r, `{"data":[{"b64_json":"abc"}]}`), nil
		})},
	}

	res, err := a.ImageGenerate(context.Background(), "gpt-image-1", &ImageRequest{
		Prompt: "make it brighter",
		N:      2,
		Size:   "1024x1024",
		References: []ImageReference{
			{Data: imgA, FileName: "a.png"},
			{Data: imgB, FileName: "b.png"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.B64s) != 1 || res.B64s[0] != "abc" {
		t.Fatalf("B64s = %#v", res.B64s)
	}
}

func TestOpenAIImageGenerateWithoutReferencesUsesGenerationsJSON(t *testing.T) {
	a := &openaiAdapter{
		baseURL: "https://upstream.test",
		apiKey:  "test-key",
		client: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/v1/images/generations" {
				return nil, fmt.Errorf("path = %s", r.URL.Path)
			}
			if got := r.Header.Get("Content-Type"); got != "application/json" {
				return nil, fmt.Errorf("content-type = %q", got)
			}
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				return nil, err
			}
			for k, want := range map[string]any{
				"model":           "gpt-image-1",
				"prompt":          "a cat",
				"n":               float64(1),
				"size":            "1024x1024",
				"response_format": "b64_json",
			} {
				if got := payload[k]; got != want {
					return nil, fmt.Errorf("%s = %#v, want %#v", k, got, want)
				}
			}

			return jsonResponse(r, `{"data":[{"url":"https://example.test/image.png"}]}`), nil
		})},
	}

	res, err := a.ImageGenerate(context.Background(), "gpt-image-1", &ImageRequest{
		Prompt: "a cat",
		N:      1,
		Size:   "1024x1024",
		Format: "b64_json",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.URLs) != 1 || res.URLs[0] != "https://example.test/image.png" {
		t.Fatalf("URLs = %#v", res.URLs)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func jsonResponse(req *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}
