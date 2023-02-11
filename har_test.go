package har_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/aidansteele/har"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/sjson"
	"net/http"
	"testing"
)

func TestRoundTripper_RoundTrip(t *testing.T) {
	buf := &bytes.Buffer{}

	rt, err := har.New(nil, buf, nil)
	require.NoError(t, err)

	c := &http.Client{Transport: rt}

	resp, err := c.Get("https://example.com")
	require.NoError(t, err)
	assert.NotNil(t, resp)

	assert.NotEmpty(t, buf.Bytes())
	fmt.Println(buf.String())
}

func TestRoundTripper_Rewrite(t *testing.T) {
	buf := &bytes.Buffer{}

	ctxKey := "messageIdKey"
	ctx := context.Background()

	rt, err := har.New(nil, buf, &har.Options{
		Creator: nil,
		Rewrite: func(request *http.Request, response *http.Response, entry json.RawMessage) json.RawMessage {
			messageId := request.Context().Value(ctxKey)
			if messageId == nil {
				return entry
			}

			entry, _ = sjson.SetBytes(entry, "_dalfoxMessageId", messageId)
			return entry
		},
	})
	require.NoError(t, err)

	c := &http.Client{Transport: rt}

	req, _ := http.NewRequestWithContext(context.WithValue(ctx, ctxKey, 123), "GET", "https://example.com", nil)
	resp, err := c.Do(req)
	require.NoError(t, err)
	assert.NotNil(t, resp)

	req, _ = http.NewRequestWithContext(context.WithValue(ctx, ctxKey, 456), "GET", "https://example.com/?a=b&c=d", nil)
	resp, err = c.Do(req)
	require.NoError(t, err)
	assert.NotNil(t, resp)

	err = rt.Close()
	require.NoError(t, err)

	assert.NotEmpty(t, buf.Bytes())
	fmt.Println(buf.String())
}
