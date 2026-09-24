package httpclient

import (
	"context"
	"net/http"
)

// FreeStorageBytes reports available bytes on the server's audio store volume.
func (c *Client) FreeStorageBytes(ctx context.Context) (uint64, error) {
	var response struct {
		FreeBytes uint64 `json:"freeBytes"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/api/v1/storage", nil, &response, http.StatusOK); err != nil {
		return 0, err
	}
	return response.FreeBytes, nil
}
