package chat

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/models/provider"
	"github.com/Tencent/WeKnora/internal/types"
	secutils "github.com/Tencent/WeKnora/internal/utils"
	"github.com/stretchr/testify/require"
)

const privateLogSentinel = "PRIVATE_PROMPT_AND_TOOL_ARGUMENT_SENTINEL"

func captureRoutineModelLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	logger.SetOutput(&output)
	logger.SetLogLevel(logger.LevelInfo)
	t.Cleanup(func() { logger.SetOutput(os.Stdout) })
	return &output
}

func loggingTestChat(t *testing.T, baseURL string) *RemoteAPIChat {
	t.Helper()
	t.Setenv("SSRF_WHITELIST", "127.0.0.1")
	t.Setenv("WEKNORA_LLM_STREAM_RAW_DUMP", "0")
	t.Setenv("WEKNORA_LLM_STREAM_RAW_DUMP_DIR", "")
	secutils.ResetSSRFWhitelistForTest()
	t.Cleanup(secutils.ResetSSRFWhitelistForTest)
	chat, err := NewRemoteAPIChat(&ChatConfig{
		Source:    types.ModelSourceRemote,
		BaseURL:   baseURL,
		ModelName: "test-model",
		ModelID:   "test-model",
		APIKey:    "private-test-token",
		Provider:  string(provider.ProviderOpenAI),
	})
	require.NoError(t, err)
	return chat
}

func TestRemoteAPIChatRoutineLogsDoNotIncludeRequestOrResponseText(t *testing.T) {
	for _, raw := range []bool{false, true} {
		name := "sdk"
		if raw {
			name = "raw"
		}
		t.Run(name, func(t *testing.T) {
			logOutput := captureRoutineModelLog(t)
			requestBody := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				requestBody <- string(body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"log-test","choices":[{"message":{"role":"assistant","content":"` + privateLogSentinel + `"},"finish_reason":"stop"}]}`))
			}))
			defer server.Close()

			chat := loggingTestChat(t, server.URL)
			ctx := logger.WithRequestID(context.Background(), "log-req-1")
			messages := []Message{{Role: "user", Content: privateLogSentinel}}
			var response *types.ChatResponse
			var err error
			if raw {
				response, err = chat.chatWithRawHTTP(ctx, "", map[string]any{
					"model":    "test-model",
					"messages": []map[string]string{{"role": "user", "content": privateLogSentinel}},
					"tools":    []map[string]any{{"type": "function", "function": map[string]any{"name": "lookup", "description": privateLogSentinel}}},
				}, nil)
			} else {
				response, err = chat.Chat(ctx, messages, nil)
			}
			require.NoError(t, err)
			require.Equal(t, privateLogSentinel, response.Content)
			require.Contains(t, <-requestBody, privateLogSentinel, "wire payload must remain unchanged")

			logs := logOutput.String()
			require.NotContains(t, logs, privateLogSentinel)
			require.NotContains(t, logs, "private-test-token")
			require.Contains(t, logs, "provider=openai")
			require.Contains(t, logs, "log-req-1")
			require.Contains(t, logs, "messages=1")
			require.Contains(t, logs, "request_bytes=")
			if raw {
				require.Contains(t, logs, "tools=1")
				require.Contains(t, logs, "headers_ms=")
			}
		})
	}
}

func TestRemoteAPIChatRoutineStreamLogsDoNotIncludeDeltas(t *testing.T) {
	for _, raw := range []bool{false, true} {
		name := "sdk"
		if raw {
			name = "raw"
		}
		t.Run(name, func(t *testing.T) {
			logOutput := captureRoutineModelLog(t)
			requestBody := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				requestBody <- string(body)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = w.Write([]byte("data: {\"id\":\"log-stream\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"" + privateLogSentinel + "\"},\"finish_reason\":null}]}\n\ndata: [DONE]\n\n"))
			}))
			defer server.Close()

			chat := loggingTestChat(t, server.URL)
			var stream <-chan types.StreamResponse
			var err error
			if raw {
				stream, err = chat.chatStreamWithRawHTTP(context.Background(), "", map[string]any{
					"model": "test-model", "stream": true,
					"messages": []map[string]string{{"role": "user", "content": privateLogSentinel}},
				}, nil)
			} else {
				stream, err = chat.ChatStream(context.Background(), []Message{{Role: "user", Content: privateLogSentinel}}, nil)
			}
			require.NoError(t, err)
			var received strings.Builder
			for item := range stream {
				received.WriteString(item.Content)
			}
			require.Contains(t, received.String(), privateLogSentinel)
			require.Contains(t, <-requestBody, privateLogSentinel, "wire payload must remain unchanged")

			logs := logOutput.String()
			require.NotContains(t, logs, privateLogSentinel)
			require.Contains(t, logs, "request_bytes=")
			require.Contains(t, logs, "headers_ms=")
			require.Contains(t, logs, "first_sse_after_headers_ms=")
			require.Contains(t, logs, "after_headers_ms=")
		})
	}
}

func TestRemoteAPIChatProviderErrorDoesNotEchoPrivateBody(t *testing.T) {
	for _, transport := range []string{"raw", "raw_stream", "sdk", "sdk_stream"} {
		t.Run(transport, func(t *testing.T) {
			logOutput := captureRoutineModelLog(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"message":"Unknown parameter: 'chat_template_kwargs'; ` + privateLogSentinel + `"}}`))
			}))
			defer server.Close()
			chat := loggingTestChat(t, server.URL)

			var err error
			switch transport {
			case "raw":
				_, err = chat.chatWithRawHTTP(context.Background(), "", map[string]any{
					"model": "test-model", "messages": []map[string]string{{"role": "user", "content": privateLogSentinel}},
				}, nil)
			case "raw_stream":
				_, err = chat.chatStreamWithRawHTTP(context.Background(), "", map[string]any{
					"model": "test-model", "stream": true,
					"messages": []map[string]string{{"role": "user", "content": privateLogSentinel}},
				}, nil)
			case "sdk":
				_, err = chat.Chat(context.Background(), []Message{{Role: "user", Content: privateLogSentinel}}, nil)
			case "sdk_stream":
				_, err = chat.ChatStream(context.Background(), []Message{{Role: "user", Content: privateLogSentinel}}, nil)
			}
			require.Error(t, err)
			require.NotContains(t, err.Error(), privateLogSentinel)
			require.NotContains(t, logOutput.String(), privateLogSentinel)
			if strings.HasPrefix(transport, "raw") {
				require.Equal(t, "API request failed with status 400", err.Error())
				require.True(t, isUnsupportedThinkingControlParameterError(err, "chat_template_kwargs"))
				require.False(t, isUnsupportedThinkingControlParameterError(err, "enable_thinking"))
			}
		})
	}
}
