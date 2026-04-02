package main

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hekmon/httplog/v3"
)

// httpError writes HTTP error response
func httpError(ctx context.Context, w http.ResponseWriter, statusCode int) {
	http.Error(w,
		generateErrorClientText(ctx, statusCode),
		statusCode,
	)
}

// generateErrorClientText generates error text with request ID
func generateErrorClientText(ctx context.Context, statusCode int) string {
	return fmt.Sprintf("%s - check honcho-embed-rp logs for more details (request id #%v)",
		http.StatusText(statusCode),
		ctx.Value(httplog.ReqIDKey),
	)
}
