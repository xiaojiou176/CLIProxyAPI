package main

import (
	"fmt"
	"log"
	"net/http"
	"time"
)

func main() {
	http.HandleFunc("/v1/completions", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("Received request: %s %s", r.Method, r.URL.Path)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		// Simulate OpenAI streaming response with tool_calls
		chunks := []string{
			`{"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"gpt-4-0613","choices":[{"index":0,"delta":{"role":"assistant","content":null,"tool_calls":[{"index":0,"id":"call_abc123","type":"function","function":{"name":"get_current_weather","arguments":""}}]},"finish_reason":null}]}`,
			`{"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"gpt-4-0613","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\""}}]},"finish_reason":null}]}`,
			`{"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"gpt-4-0613","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"location"}}]},"finish_reason":null}]}`,
			`{"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"gpt-4-0613","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\": \"Boston\"}"}}]},"finish_reason":null}]}`,
			`{"id":"chatcmpl-123","object":"chat.completion.chunk","created":1694268190,"model":"gpt-4-0613","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			`[DONE]`,
		}

		for _, chunk := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", chunk)
			w.(http.Flusher).Flush()
			time.Sleep(100 * time.Millisecond)
		}
	})

	log.Println("Mock Upstream Server listening on :8319")
	log.Fatal(http.ListenAndServe(":8319", nil))
}
