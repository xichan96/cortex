package hostconfig

func NeedLLMSetup(host *HostAppConfig) bool {
	if host == nil {
		return true
	}
	p := host.Server.Cortex.LLM.Provider
	if p == "" || p == "mock" {
		return true
	}
	pc := LLMProvider(&host.Server.Cortex.LLM, p)
	return pc == nil || pc.APIKey == ""
}

func LLMProvider(llm *LLMConfig, provider string) *ProviderConfig {
	if llm == nil {
		return nil
	}
	if llm.Providers != nil {
		if v, ok := llm.Providers[provider]; ok {
			return &v
		}
	}
	switch provider {
	case "openai":
		return &llm.OpenAI
	case "deepseek":
		return &llm.DeepSeek
	case "volce":
		return &llm.Volce
	case "gemini":
		return &llm.Gemini
	case "anthropic":
		return &llm.Anthropic
	default:
		return nil
	}
}

func HostLLMProvider(host *HostAppConfig, provider string) *ProviderConfig {
	if host == nil {
		return nil
	}
	return LLMProvider(&host.Server.Cortex.LLM, provider)
}
