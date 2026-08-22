package service

// These are curated official model IDs used when an upstream has not yet
// supplied an account-specific model mapping. They are candidates only:
// custom probe and account models remain valid and are merged separately.
var kimiOfficialModelIDs = []string{
	"moonshot-v1-8k", "moonshot-v1-32k", "moonshot-v1-128k",
	"kimi-latest", "kimi-for-coding", "kimi-coding",
	"kimi-k2", "kimi-k2-thinking", "kimi-k2.5", "kimi-k2.6",
	"kimi-k2-0711", "kimi-k2-0905-preview",
}

var zhipuOfficialModelIDs = []string{
	"glm-4", "glm-4v", "glm-4-plus", "glm-4-0520",
	"glm-4-air", "glm-4-airx", "glm-4-long", "glm-4-flash",
	"glm-4v-plus", "glm-4.5", "glm-4.5-air", "glm-4.6", "glm-4.7",
	"glm-5", "glm-5.1", "glm-5.2", "glm-5-turbo",
	"glm-3-turbo", "glm-4-alltools",
	"chatglm_turbo", "chatglm_pro", "chatglm_std", "chatglm_lite",
	"cogview-3", "cogvideo",
}

var deepseekOfficialModelIDs = []string{
	"deepseek-chat", "deepseek-coder", "deepseek-reasoner",
	"deepseek-v3", "deepseek-v3-0324", "deepseek-v3.2", "deepseek-v3-2-251201",
	"deepseek-r1", "deepseek-r1-0528",
	"deepseek-r1-distill-qwen-32b", "deepseek-r1-distill-qwen-14b", "deepseek-r1-distill-qwen-7b",
	"deepseek-r1-distill-llama-70b", "deepseek-r1-distill-llama-8b",
	"deepseek-v4-flash", "deepseek-v4-pro",
}
