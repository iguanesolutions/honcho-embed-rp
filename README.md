# honcho-embed-rp

Honcho Embed Reverse Proxy is a lightweight HTTP reverse proxy that intercepts OpenAI-compatible `/v1/embeddings` API requests. It automatically rewrites the model name and adds a `dimensions` parameter (default: 1536) before forwarding requests to the backend embedding server.

## Honcho Integration Trick

This proxy implements the workaround described in [plastic-labs/honcho#404](https://github.com/plastic-labs/honcho/issues/404#issuecomment-4119420068) for running fully local Honcho deployments with custom embedding models.

### The Problem

Honcho's embedding configuration has several hardcodes:
- Only supports `openai`, `gemini`, or `openrouter` providers for custom base URLs
- When using `openrouter` provider, model name is hardcoded to `openai/text-embedding-3-large`
- Database schema and code expect exactly 1536-dimensional embeddings

### The Solution

This reverse proxy sits between Honcho and your embedding server (e.g., vLLM), performing these transformations:

1. **Model name rewriting**: Honcho requests `openai/text-embedding-3-large` → proxy rewrites to your actual model (e.g., `Qwen/Qwen3-Embedding-4B`)
2. **Dimensions injection**: Adds `dimensions: 1536` to all requests (configurable, default: 1536 for Honcho compatibility)
3. **Response model fixing**: Rewrites model name back in responses so Honcho sees what it expects

This allows you to use any embedding model with Honcho without modifying vLLM's `--served-model-name` or Honcho's source code.

## Core Functionality

This proxy's primary purpose is to:

1. **Intercept `/v1/embeddings` requests** from clients
2. **Rewrite the model name** from the client-facing model name to the actual backend model name
3. **Add dimensions parameter** set to 1536 to all embedding requests
4. **Restore the original model name** in the response before sending it back to the client
5. **Pass through all other requests** unchanged to the backend

## Installation

Requirements: Go 1.24.2 or later

```bash
go build -o honcho-embed-rp .
```

## Usage

```bash
./honcho-embed-rp \
  -target "http://127.0.0.1:8000" \
  -served-model "Qwen/Qwen3-Embedding-4B"
```

Or using environment variables:

```bash
export HONCHOEMBEDRP_TARGET="http://127.0.0.1:8000"
export HONCHOEMBEDRP_SERVED_MODEL_NAME="Qwen/Qwen3-Embedding-4B"
./honcho-embed-rp
```

## Configuration

Configure the proxy using command-line flags or environment variables:

| Flag | Environment Variable | Default | Description |
|------|---------------------|---------|-------------|
| `-listen` | `HONCHOEMBEDRP_LISTEN` | `0.0.0.0` | IP address to listen on |
| `-port` | `HONCHOEMBEDRP_PORT` | `9000` | Port to listen on |
| `-target` | `HONCHOEMBEDRP_TARGET` | `http://127.0.0.1:8000` | Backend target URL |
| `-loglevel` | `HONCHOEMBEDRP_LOGLEVEL` | `INFO` | Log level (COMPLETE, DEBUG, INFO, WARN, ERROR) |
| `-served-model` | `HONCHOEMBEDRP_SERVED_MODEL_NAME` | (required) | Backend model name to use in outgoing requests |
| `-dimensions` | `HONCHOEMBEDRP_DIMENSIONS` | `1536` | Embedding dimensions (1536 for Honcho compatibility) |

## Request Routing

- **`POST /v1/embeddings`**: Transformed (model name rewritten, dimensions=1536 added)
- **`GET /health`**: Health check endpoint (returns `{"status":"healthy"}`)
- **All other paths**: Passed through unchanged to the backend

## Example Request/Response

### Standard Usage

**Client Request (Honcho sends this):**
```json
POST /v1/embeddings
{
  "model": "openai/text-embedding-3-large",
  "input": "Hello, world!"
}
```

**Backend Request (after transformation):**
```json
POST /v1/embeddings
{
  "model": "Qwen/Qwen3-Embedding-4B",
  "input": "Hello, world!",
  "dimensions": 1536
}
```

**Client Response (model name restored for Honcho):**
```json
{
  "object": "list",
  "data": [...],
  "model": "openai/text-embedding-3-large",
  "usage": {...}
}
```

### Honcho Integration Example

**vLLM embedding server (Docker Compose):**
```yaml
services:
  vllm-embedding:
    image: vllm/vllm-openai:latest
    command:
      - Qwen/Qwen3-Embedding-4B
      - --port
      - "8000"
      - --gpu-memory-utilization
      - "0.5"
      - --hf-overrides
      - '{"is_matryoshka": true, "matryoshka_dimensions": [1536]}'
    deploy:
      resources:
        reservations:
          devices:
            - driver: nvidia
              count: 1
              capabilities: [gpu]
```

**honcho-embed-rp (Docker Compose):**
```yaml
services:
  honcho-embed-rp:
    image: honcho-embed-rp:latest
    environment:
      - HONCHOEMBEDRP_TARGET=http://vllm-embedding:8000
      - HONCHOEMBEDRP_SERVED_MODEL_NAME=Qwen/Qwen3-Embedding-4B
      - HONCHOEMBEDRP_DIMENSIONS=1536
    ports:
      - "9000:9000"
```

**Honcho environment variables:**
```bash
# Point OpenAI-compatible provider to the proxy
LLM_OPENAI_COMPATIBLE_BASE_URL=http://honcho-embed-rp:9000/v1
LLM_OPENAI_COMPATIBLE_API_KEY=sk-no-key-required

# Use openrouter provider (supports custom base URL)
# Honcho will request model: openai/text-embedding-3-large
LLM_EMBEDDING_PROVIDER=openrouter

# vLLM provider for LLM calls (separate endpoint)
DERIVER_PROVIDER=vllm
DERIVER_MODEL="your-llm-model-name"
LLM_VLLM_BASE_URL=http://your-vllm-llm:8000/v1
LLM_VLLM_API_KEY=your-api-key
```

With this setup:
- Honcho requests embeddings from `openai/text-embedding-3-large` at the proxy (hardcoded by openrouter provider)
- Proxy rewrites to `Qwen/Qwen3-Embedding-4B` with `dimensions: 1536`
- vLLM serves the Qwen model with Matryoshka embeddings at 1536 dimensions
- Response model name is rewritten back to `openai/text-embedding-3-large` for Honcho compatibility

## Health Check

- **`GET /health`**: Returns `{"status":"healthy"}` for Docker health checks

## Log Levels

The proxy supports the following log levels:

| Level | Description |
|-------|-------------|
| `COMPLETE` | Most verbose - includes full HTTP request/response dumps |
| `DEBUG` | Debug information including parameter application details |
| `INFO` | General operational information |
| `WARN` | Warning messages |
| `ERROR` | Error messages only |

When set to `COMPLETE`, the proxy will log full HTTP request and response bodies, which is useful for debugging but very verbose.

⚠️ **Privacy Warning**: Embedding requests may contain sensitive or personal data. The `COMPLETE` log level will expose all this data in plaintext. Only enable it in secure, non-production environments or ensure logs are properly secured and retained temporarily.

## License

MIT License - see [LICENSE](LICENSE) file for details.