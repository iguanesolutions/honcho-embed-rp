package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
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
		logger.Info("embeddings handler called",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
		)

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
		// Construct backend URL: target base + /embeddings (ignore incoming request path)
		outreq.URL.Scheme = target.Scheme
		outreq.URL.Host = target.Host
		outreq.URL.Path = path.Join(target.Path, "/embeddings")
		outreq.URL.RawPath = "" // Clear RawPath, let Go handle encoding via EscapedPath
		outreq.URL.RawQuery = target.RawQuery
		outreq.Host = target.Host // Explicitly set Host header for backend
		logger.Debug("backend URL constructed",
			slog.String("target_base", target.String()),
			slog.String("backend_path", outreq.URL.Path),
			slog.String("final_url", outreq.URL.String()),
		)
		outreq.Body = io.NopCloser(bytes.NewReader(requestBody))
		outreq.ContentLength = int64(len(requestBody))
		outreq.Header.Del("Content-Length") // Remove old header, let Go set it from ContentLength field
		outreq.RequestURI = ""

		// Send request to backend
		logger.Debug("sending request to backend",
			slog.String("method", outreq.Method),
			slog.String("url", outreq.URL.String()),
			slog.String("content_length", outreq.Header.Get("Content-Length")),
			slog.String("content_type", outreq.Header.Get("Content-Type")),
			slog.String("authorization", outreq.Header.Get("Authorization")),
			slog.String("user_agent", outreq.Header.Get("User-Agent")),
			slog.String("host", outreq.Host),
			slog.Any("all_headers", outreq.Header),
		)
		outResp, err := httpCli.Do(outreq)
		if err != nil {
			logger.Error("failed to send upstream request", slog.Any("error", err))
			httpError(ctx, w, http.StatusBadGateway)
			return
		}
		defer outResp.Body.Close()
		logger.Debug("backend response received",
			slog.Int("status_code", outResp.StatusCode),
			slog.String("status", outResp.Status),
			slog.String("content_length", outResp.Header.Get("Content-Length")),
			slog.String("content_type", outResp.Header.Get("Content-Type")),
		)
		// Read response body for debugging
		respBody, readErr := io.ReadAll(outResp.Body)
		if readErr == nil && outResp.StatusCode >= 400 {
			logger.Error("backend returned error",
				slog.Int("status_code", outResp.StatusCode),
				slog.String("body", string(respBody)),
			)
			// Restore body for downstream processing
			outResp.Body = io.NopCloser(bytes.NewReader(respBody))
		} else if readErr == nil {
			// Restore body for downstream processing
			outResp.Body = io.NopCloser(bytes.NewReader(respBody))
		}

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
