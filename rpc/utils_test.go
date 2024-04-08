package rpc

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func Test_GetClientIPFromXFF(t *testing.T) {

	tt := []struct {
		name       string
		headerList []string
		expected   string
	}{
		{
			name:       "Heaer contains just one IP",
			headerList: []string{"192.168.0.1"},
			expected:   "192.168.0.1",
		},
		{
			name:       "Heaer contains just one IP",
			headerList: []string{"127.0.0.1", "192.168.0.1"},
			expected:   "127.0.0.1",
		},
	}

	for _, v := range tt {
		t.Run(v.name, func(t *testing.T) {

			req := httptest.NewRequest(http.MethodGet, "/", strings.NewReader("{}"))

			for _, header := range v.headerList {
				req.Header.Add("X-Forwarded-For", header)
			}

			ctx := &gin.Context{
				Request: req,
			}

			require.Equal(t, v.expected, GetClientIPFromXFF(ctx))
		})
	}
}
