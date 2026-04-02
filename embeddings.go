package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"

	"github.com/hekmon/httplog/v3"
)

// EmbeddingsRequest represents the request body for /v1/embeddings endpoint
type EmbeddingsRequest struct {
	Model      string `json:"model"`
	Input      any    `json:"input"`
	Dimensions int    `json:"dimensions,omitempty"`
	Encoding   string `json:"encoding_format,omitempty"`
}

// embeddings handles the /v1/embeddings endpoint by intercepting requests,
// rewriting the model name, and adding dimensions parameter
func embeddings(httpCli *http.Client, target *url.URL, servedModel string, dimensions int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Prepare
		logger := logger.With(httplog.GetReqIDSLogAttr(r.Context()))
		ctx := r.Context()

		// Read request body
		requestBody, err := io.ReadAll(r.Body)
		if err != nil {
			logger.Error("failed to read body", slog.String("error", err.Error()))
			httpError(ctx, w, http.StatusInternalServerError)
			return
		}

		// Parse request body
		var reqData map[string]any
		err = json.Unmarshal(requestBody, &reqData)
		if err != nil {
			logger.Error("failed to parse body as JSON", slog.String("error", err.Error()))
			httpError(ctx, w, http.StatusBadRequest)
			return
		}

		// Validate model field exists
		if _, ok := reqData["model"]; !ok {
			logger.Error("missing model in request body")
			httpError(ctx, w, http.StatusBadRequest)
			return
		}

		// Validate input field exists
		if _, ok := reqData["input"]; !ok {
			logger.Error("missing input in request body")
			httpError(ctx, w, http.StatusBadRequest)
			return
		}

		// Add dimensions parameter
		reqData["dimensions"] = dimensions
		logger.Debug("added dimensions parameter", slog.Int("dimensions", dimensions))

		// Override model name for backend
		originalModel := reqData["model"].(string)
		reqData["model"] = servedModel
		logger.Debug("rewrote model name",
			slog.String("original", originalModel),
			slog.String("replacement", servedModel),
		)

		// Marshal request body
		requestBody, err = json.Marshal(reqData)
		if err != nil {
			logger.Error("failed to marshal request body", slog.Any("error", err))
			httpError(ctx, w, http.StatusInternalServerError)
			return
		}

		logger.Debug("forwarding embeddings request to backend", slog.String("body", string(requestBody)))

		// Track modified requests
		modifiedRequests.Add(1)

		// Prepare outgoing request
		outreq := r.Clone(ctx)
		rewriteRequestURL(outreq, target)
		outreq.Body = io.NopCloser(bytes.NewReader(requestBody))
		outreq.ContentLength = int64(len(requestBody))
		outreq.RequestURI = ""

		// Send request to backend
		outResp, err := httpCli.Do(outreq)
		if err != nil {
			logger.Error("failed to send upstream request", slog.Any("error", err))
			httpError(ctx, w, http.StatusBadGateway)
			return
		}
		defer outResp.Body.Close()

		// Copy response headers
		for header, values := range outResp.Header {
			for _, value := range values {
				w.Header().Add(header, value)
			}
		}

		// Read and fix response (rewrite model name back to original)
		responseBody, err := io.ReadAll(outResp.Body)
		if err != nil {
			logger.Error("failed to read response body", slog.String("error", err.Error()))
			httpError(ctx, w, http.StatusInternalServerError)
			return
		}

		// Fix model name in response
		responseBody = fixEmbeddingsModelName(responseBody, originalModel, logger)

		w.Header().Set("Content-Length", strconv.Itoa(len(responseBody)))
		w.WriteHeader(outResp.StatusCode)
		if _, err = w.Write(responseBody); err != nil {
			logger.Error("failed to write response", slog.String("error", err.Error()))
		}
	}
}

// fixEmbeddingsModelName replaces the backend model name with the original model name
// that the client originally requested
func fixEmbeddingsModelName(responseBody []byte, originalModel string, logger *slog.Logger) []byte {
	// Try to parse the response as JSON
	var data map[string]any
	if err := json.Unmarshal(responseBody, &data); err != nil {
		// Not valid JSON, return as-is
		return responseBody
	}

	// Check if model field exists and replace it
	if _, ok := data["model"]; ok {
		logger.Debug("fixing model name in embeddings response",
			slog.String("original", data["model"].(string)),
			slog.String("replacement", originalModel),
		)
		data["model"] = originalModel

		// Re-marshal the response
		fixedBody, err := json.Marshal(data)
		if err != nil {
			logger.Error("failed to marshal response with fixed model name", slog.Any("error", err))
			return responseBody
		}
		return fixedBody
	}

	return responseBody
}
