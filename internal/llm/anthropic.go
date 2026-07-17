package llm

// NewAnthropic returns a provider hitting the Anthropic first-party API.
func NewAnthropic(baseURL, apiKey string) Provider {
	return newMessagesClient("anthropic", baseURL, map[string]string{
		"x-api-key":         apiKey,
		"anthropic-version": "2023-06-01",
	})
}
