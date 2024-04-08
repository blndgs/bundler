package rpc

import (
	"bytes"
	"io"

	"github.com/gin-gonic/gin"
)

type reusableReader struct {
	*bytes.Reader
	io.Closer
}

func (r *reusableReader) Close() error {
	return nil
}

// readBody reads the body of a gin context and allows it to be read again.
func readBody(c *gin.Context) ([]byte, error) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return nil, err
	}
	c.Request.Body = &reusableReader{bytes.NewReader(body), c.Request.Body}
	return body, nil
}
