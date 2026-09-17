//go:build unit

package service

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/LuckyKuang/sub2api-plus/internal/config"
	"github.com/LuckyKuang/sub2api-plus/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAICodexProxyTimezoneForID(t *testing.T) {
	raw := "12=America/New_York, 27=Asia/Tokyo; *=America/Los_Angeles"
	require.Equal(t, "America/New_York", openAICodexProxyTimezoneForID(raw, 12))
	require.Equal(t, "Asia/Tokyo", openAICodexProxyTimezoneForID(raw, 27))
	require.Equal(t, "America/Los_Angeles", openAICodexProxyTimezoneForID(raw, 99))
}

func TestResolveOpenAICodexEnvironmentTimezone(t *testing.T) {
	svc := &OpenAIGatewayService{
		cfg: &config.Config{
			Gateway: config.GatewayConfig{
				OpenAICodexDirectTimezone: "America/Los_Angeles",
				OpenAICodexProxyTimezones: "12=America/New_York",
			},
		},
	}

	t.Run("direct", func(t *testing.T) {
		account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
		timezoneName, currentDate, ok := svc.resolveOpenAICodexEnvironmentTimezone(account)
		require.True(t, ok)
		require.Equal(t, "America/Los_Angeles", timezoneName)
		require.NotEmpty(t, currentDate)
	})

	t.Run("mapped proxy", func(t *testing.T) {
		proxyID := int64(12)
		account := &Account{
			Platform: PlatformOpenAI,
			Type:     AccountTypeOAuth,
			ProxyID:  &proxyID,
			Proxy:    &Proxy{ID: proxyID},
		}
		timezoneName, currentDate, ok := svc.resolveOpenAICodexEnvironmentTimezone(account)
		require.True(t, ok)
		require.Equal(t, "America/New_York", timezoneName)
		require.NotEmpty(t, currentDate)
	})

	t.Run("unmapped proxy does not use direct timezone", func(t *testing.T) {
		proxyID := int64(13)
		account := &Account{
			Platform: PlatformOpenAI,
			Type:     AccountTypeOAuth,
			ProxyID:  &proxyID,
			Proxy:    &Proxy{ID: proxyID},
		}
		_, _, ok := svc.resolveOpenAICodexEnvironmentTimezone(account)
		require.False(t, ok)
	})

	t.Run("non OpenAI OAuth account is excluded", func(t *testing.T) {
		account := &Account{Platform: PlatformGrok, Type: AccountTypeOAuth}
		_, _, ok := svc.resolveOpenAICodexEnvironmentTimezone(account)
		require.False(t, ok)
	})

	t.Run("implicit legacy OpenAI OAuth account remains supported", func(t *testing.T) {
		account := &Account{Type: AccountTypeOAuth}
		timezoneName, _, ok := svc.resolveOpenAICodexEnvironmentTimezone(account)
		require.True(t, ok)
		require.Equal(t, "America/Los_Angeles", timezoneName)
	})
}

func TestRewriteOpenAICodexEnvironmentTimezoneRaw(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.6-sol",
		"input":[
			{
				"type":"message",
				"role":"user",
				"content":[
					{
						"type":"input_text",
						"text":"<environment_context>\n  <cwd>/workspace</cwd>\n  <current_date>2026-01-01</current_date>\n  <timezone>Asia/Shanghai</timezone>\n</environment_context>"
					}
				]
			},
			{
				"type":"message",
				"role":"user",
				"content":[
					{
						"type":"input_text",
						"text":"Do not rewrite this ordinary example: <timezone>Asia/Shanghai</timezone>"
					}
				]
			},
			{
				"type":"message",
				"role":"user",
				"content":[
					{
						"type":"input_text",
						"text":"Please explain this quoted payload:\n<environment_context>\n  <current_date>2026-01-01</current_date>\n  <timezone>Asia/Shanghai</timezone>\n</environment_context>"
					}
				]
			}
		]
	}`)

	rewritten, changed, err := rewriteOpenAICodexEnvironmentTimezoneRaw(
		body,
		"America/New_York",
		"2026-09-16",
	)
	require.NoError(t, err)
	require.True(t, changed)

	envText := gjson.GetBytes(rewritten, "input.0.content.0.text").String()
	require.Contains(t, envText, "<timezone>America/New_York</timezone>")
	require.Contains(t, envText, "<current_date>2026-09-16</current_date>")

	ordinaryText := gjson.GetBytes(rewritten, "input.1.content.0.text").String()
	require.Contains(t, ordinaryText, "<timezone>Asia/Shanghai</timezone>")

	quotedText := gjson.GetBytes(rewritten, "input.2.content.0.text").String()
	require.Contains(t, quotedText, "<timezone>Asia/Shanghai</timezone>")
	require.Contains(t, quotedText, "<current_date>2026-01-01</current_date>")
}

func TestRewriteOpenAICodexEnvironmentTimezoneForAccount(t *testing.T) {
	svc := &OpenAIGatewayService{
		cfg: &config.Config{
			Gateway: config.GatewayConfig{
				OpenAICodexDirectTimezone: "America/Los_Angeles",
			},
		},
	}
	body := []byte(`{"input":[{"role":"user","content":"<environment_context><current_date>2026-01-01</current_date><timezone>Asia/Shanghai</timezone></environment_context>"}]}`)

	rewritten, changed, err := svc.rewriteOpenAICodexEnvironmentTimezoneForAccount(
		body,
		&Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth},
	)
	require.NoError(t, err)
	require.True(t, changed)
	require.Contains(t, string(rewritten), `<timezone>America/Los_Angeles</timezone>`)

	untouched, changed, err := svc.rewriteOpenAICodexEnvironmentTimezoneForAccount(
		body,
		&Account{Platform: PlatformGrok, Type: AccountTypeOAuth},
	)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, body, untouched)
}

func TestRewriteOpenAICodexEnvironmentTimezoneDiagnosticLog(t *testing.T) {
	logSink, restore := captureStructuredLog(t)
	defer restore()

	svc := &OpenAIGatewayService{
		cfg: &config.Config{
			Gateway: config.GatewayConfig{
				OpenAICodexProxyTimezones: "12=America/New_York",
			},
		},
	}
	proxyID := int64(12)
	account := &Account{
		ID:       42,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		ProxyID:  &proxyID,
		Proxy:    &Proxy{ID: proxyID},
	}
	body := []byte(`{
		"input":[
			{"role":"user","content":[{"type":"input_text","text":"<environment_context><current_date>2026-01-01</current_date><timezone>Asia/Shanghai</timezone></environment_context>"}]},
			{"role":"user","content":"Authorization: Bearer request-body-secret; Cookie: session=request-body-cookie"}
		]
	}`)
	ctx := context.WithValue(context.Background(), ctxkey.RequestID, "req-timezone-1")

	_, expectedDate, ok := svc.resolveOpenAICodexEnvironmentTimezone(account)
	require.True(t, ok)
	rewritten, changed, err := svc.rewriteOpenAICodexEnvironmentTimezoneForAccountAndLog(ctx, body, account)
	require.NoError(t, err)
	require.True(t, changed)
	require.Contains(t, string(rewritten), `<timezone>America/New_York</timezone>`)

	require.True(t, logSink.ContainsMessageAtLevel("openai_codex_timezone_rewrite", "info"))
	require.True(t, logSink.ContainsFieldValue("request_id", "req-timezone-1"))
	require.True(t, logSink.ContainsFieldValue("account_id", "42"))
	require.True(t, logSink.ContainsFieldValue("proxy_id", "12"))
	require.True(t, logSink.ContainsFieldValue("timezone_rewrite_changed", "true"))
	require.True(t, logSink.ContainsFieldValue("timezone_before", "Asia/Shanghai"))
	require.True(t, logSink.ContainsFieldValue("timezone_after", "America/New_York"))
	require.True(t, logSink.ContainsFieldValue("current_date_before", "2026-01-01"))
	require.True(t, logSink.ContainsFieldValue("current_date_after", expectedDate))

	logSink.mu.Lock()
	defer logSink.mu.Unlock()
	for _, event := range logSink.events {
		serialized := event.Message + fmt.Sprint(event.Fields)
		require.NotContains(t, serialized, "request-body-secret")
		require.NotContains(t, serialized, "request-body-cookie")
		require.NotContains(t, strings.ToLower(serialized), "authorization")
		require.NotContains(t, strings.ToLower(serialized), "cookie")
	}
}

func TestOpenAICodexTimezoneDiagnosticRejectsUntrustedTagValues(t *testing.T) {
	logSink, restore := captureStructuredLog(t)
	defer restore()

	svc := &OpenAIGatewayService{
		cfg: &config.Config{
			Gateway: config.GatewayConfig{
				OpenAICodexDirectTimezone: "America/Los_Angeles",
			},
		},
	}
	account := &Account{ID: 43, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	body := []byte(`{"input":[{"role":"user","content":"<environment_context><current_date>Cookie date-secret</current_date><timezone>Bearer timezone-secret</timezone></environment_context>"}]}`)

	_, changed, err := svc.rewriteOpenAICodexEnvironmentTimezoneForAccountAndLog(context.Background(), body, account)
	require.NoError(t, err)
	require.True(t, changed)
	require.True(t, logSink.ContainsFieldValue("timezone_after", "America/Los_Angeles"))

	logSink.mu.Lock()
	defer logSink.mu.Unlock()
	for _, event := range logSink.events {
		serialized := event.Message + fmt.Sprint(event.Fields)
		require.NotContains(t, serialized, "timezone-secret")
		require.NotContains(t, serialized, "date-secret")
	}
}

func TestBuildOpenAIWSCreatePayloadRewritesTimezone(t *testing.T) {
	svc := &OpenAIGatewayService{
		cfg: &config.Config{
			Gateway: config.GatewayConfig{
				OpenAICodexProxyTimezones: "12=America/New_York",
			},
		},
	}
	proxyID := int64(12)
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		ProxyID:  &proxyID,
		Proxy:    &Proxy{ID: proxyID},
	}
	reqBody := map[string]any{
		"input": []any{
			map[string]any{
				"type": "message",
				"role": "user",
				"content": []any{
					map[string]any{
						"type": "input_text",
						"text": "<environment_context>\n  <current_date>2026-01-01</current_date>\n  <timezone>Asia/Shanghai</timezone>\n</environment_context>",
					},
				},
			},
		},
	}

	payload := svc.buildOpenAIWSCreatePayload(reqBody, account)
	input, ok := payload["input"].([]any)
	require.True(t, ok)
	require.Len(t, input, 1)
	item, ok := input[0].(map[string]any)
	require.True(t, ok)
	content, ok := item["content"].([]any)
	require.True(t, ok)
	require.Len(t, content, 1)
	part, ok := content[0].(map[string]any)
	require.True(t, ok)
	text, ok := part["text"].(string)
	require.True(t, ok)
	require.Contains(t, text, "<timezone>America/New_York</timezone>")
}
