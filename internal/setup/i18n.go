// Package setup i18n — bilingual strings for the interactive setup wizard.
package setup

// Lang represents a language selection.
type Lang int

const (
	LangZH Lang = iota // 中文
	LangEN             // English
)

// langStr holds all user-facing strings for one language.
type langStr struct {
	// --- Banner ---
	Title       string
	NavHint     string
	BackCmd     string
	QuitCmd     string
	CancelMsg   string
	CompleteMsg string

	// --- Error / Confirm ---
	ErrorRetry string
	SavePrompt string
	SaveFailed string
	SaveOK     string
	NoMCPJSON  string

	// --- Data dir ---
	DataDirTitle string
	DataDirPrompt string

	// --- Default KB ---
	DefaultKBTitle string
	DefaultKBHelp  string
	DefaultKBPrompt string

	// --- Embedder ---
	EmbedderTitle  string
	EmbedderDesc   string
	EmbedderEnable string
	EmbedderURL    string
	EmbedderModel  string
	EmbedderDim    string
	EmbedderAPIKey string
	EmbedTestTitle string
	EmbedTestOK    string
	EmbedTestFail  string
	EmbedRetry     string
	EmbedSkipped   string
	EmbedTestConn  string

	// --- Reranker ---
	RerankerTitle  string
	RerankerDesc   string
	RerankerEnable string
	RerankerURL    string
	RerankerModel  string
	RerankerAPIKey string
	RerankerTimeout string
	RerankTestTitle string
	RerankTestOK    string
	RerankTestFail  string
	RerankRetry     string
	RerankSkipped   string
	RerankTestConn  string

	// --- Rerank limit ---
	RerankLimitTitle string
	RerankLimitDesc  string
	RerankLimitPrompt string

	// --- Doc Parser ---
	DocParserTitle  string
	DocParserDesc   string
	DocParserEnable string
	DocParserURL    string
	DocParserAPIKey string
	DocParserTimeout string

	// --- GPU Scheduler ---
	GPUSchedTitle    string
	GPUSchedDesc     string
	GPUSchedURLHint  string
	GPUSchedNoModels string
	GPUSchedEnable   string
	GPUSchedEmbedTitle string
	GPUSchedEmbedSleep string
	GPUSchedRerankTitle string
	GPUSchedRerankSleep string
	GPUSchedDocParserTitle string
	GPUSchedDocParserSleep string
	GPUSchedTimeout     string

	// --- Manage port ---
	ManagePortTitle  string
	ManagePortDesc   string
	ManagePortPrompt string

	// --- Logging ---
	LogTitle       string
	LogFilePath    string
	LogLevelPrompt string

	// --- Summary ---
	SummaryHeader  string
	SummaryDataDir string
	SummaryDefaultKB string
	SummaryEmbedding string
	SummaryReranker  string
	SummaryRerankLimit string
	SummaryGPUSched   string
	SummaryManagePort string
	SummaryLogLevel   string
	SummaryEnabled    string
	SummaryNotCfg     string
	SummaryNone       string
	SummaryEmbedSleep string
	SummaryRerankSleep string
	SummaryDocParser   string
	SummaryDocParserSleep string
}

var zhStrs = langStr{
	// --- Banner ---
	Title:       "knowledge-mcp 交互式配置向导",
	NavHint:     "每步可输入 b 返回上一步，输入 q 退出",
	BackCmd:     "b",
	QuitCmd:     "q",
	CancelMsg:   "\n配置已取消。",
	CompleteMsg: "\n✓ Setup complete. 知识库服务已配置完毕。",

	// --- Error / Confirm ---
	ErrorRetry: "错误: %v\n按 Enter 重试...",
	SavePrompt: "\n确认保存以上配置？(Y/n): ",
	SaveFailed: "保存配置文件失败: %v\n",
	SaveOK:     "✓ 配置已保存到 %s\n",
	NoMCPJSON:  "(未找到 .mcp.json，跳过更新)",

	// --- Data dir ---
	DataDirTitle:  "\n--- 数据目录 ---",
	DataDirPrompt: "知识库数据存储目录",

	// --- Default KB ---
	DefaultKBTitle:  "\n--- 默认知识库 ---",
	DefaultKBHelp:   "(可选，回车跳过。如设置，AI 工具调用时默认使用此知识库)",
	DefaultKBPrompt: "默认知识库名称",

	// --- Embedder ---
	EmbedderTitle:  "\n--- Embedding 模型配置 ---",
	EmbedderDesc:   "用于将文档转为向量，支持任何 OpenAI 兼容 API（如 Ollama）",
	EmbedderEnable: "是否配置 embedding 模型",
	EmbedderURL:    "Embedding API 地址",
	EmbedderModel:  "Embedding 模型名称",
	EmbedderDim:    "向量维度（回车=自动检测）",
	EmbedderAPIKey: "API Key（可选，回车跳过）",
	EmbedTestTitle: "[测试] 正在测试 embedding API 连接...",
	EmbedTestOK:    "✓ Embedder 连接成功 (dim=%d)\n",
	EmbedTestFail:  "✗ 连接失败: %v\n",
	EmbedRetry:     "(r) 重试 (s) 跳过 (b) 返回: ",
	EmbedSkipped:   "已跳过 embedding 配置",
	EmbedTestConn:  "Embedding API 连接测试",

	// --- Reranker ---
	RerankerTitle:  "\n--- Reranker 模型配置 ---",
	RerankerDesc:   "用于二次排序提高搜索精度。支持 Infinity 或 Cohere 兼容 API",
	RerankerEnable: "是否配置 reranker 模型",
	RerankerURL:    "Reranker API 地址",
	RerankerModel:  "Reranker 模型名称",
	RerankerAPIKey: "API Key（可选，回车跳过）",
	RerankerTimeout: "请求超时时间（如 30s, 60s）",
	RerankTestTitle: "[测试] 正在测试 reranker API 连接...",
	RerankTestOK:    "✓ Reranker 连接成功\n",
	RerankTestFail:  "✗ 连接失败: %v\n",
	RerankRetry:     "(r) 重试 (s) 跳过 (b) 返回: ",
	RerankSkipped:   "已跳过 reranker 配置",
	RerankTestConn:  "Reranker API 连接测试",

	// --- Rerank limit ---
	RerankLimitTitle:  "\n--- Rerank 候选数限制 ---",
	RerankLimitDesc:   "每次重排序最多处理多少个候选文档",
	RerankLimitPrompt: "候选数",

	// --- Doc Parser ---
	DocParserTitle:  "\n--- 文档解析 API 配置 ---",
	DocParserDesc:   "用于解析非纯文本文档（PDF/DOCX 等）。配置外部 HTTP API 优先，API 不可用时自动回退本地 tabula",
	DocParserEnable: "是否配置文档解析 API",
	DocParserURL:    "文档解析 API 地址",
	DocParserAPIKey: "API Key（可选，回车跳过）",
	DocParserTimeout: "请求超时时间（如 30s, 120s）",

	// --- GPU Scheduler ---
	GPUSchedTitle:    "\n--- GPU 调度器 ---",
	GPUSchedDesc:     "当 embedding 和 reranker 模型共享 GPU 时，调度器协调两者的睡眠/唤醒",
	GPUSchedURLHint:  "需要提供两个模型各自的睡眠/唤醒 API，用于在 GPU 内存中切换模型",
	GPUSchedNoModels: "⚠ 未配置任何模型，GPU 调度器需要同时配置 embedding 和 reranker\n   请先在上一步配置两个模型再启用调度器",
	GPUSchedEnable:   "是否启用 GPU 调度器",
	GPUSchedEmbedTitle: "\n--- Embedding 模型睡眠 API ---",
	GPUSchedEmbedSleep: "Embedding 睡眠 API 地址",
	GPUSchedRerankTitle: "\n--- Reranker 模型睡眠 API ---",
	GPUSchedRerankSleep: "Reranker 睡眠 API 地址",
	GPUSchedDocParserTitle: "\n--- 文档解析模型睡眠 API ---",
	GPUSchedDocParserSleep: "文档解析模型睡眠 API 地址",
	GPUSchedTimeout:     "调度请求超时时间",

	// --- Manage port ---
	ManagePortTitle:  "\n--- 管理界面端口 ---",
	ManagePortDesc:   "知识库的管理 UI 监听端口",
	ManagePortPrompt: "管理端口",

	// --- Logging ---
	LogTitle:       "\n--- 日志配置 ---",
	LogFilePath:    "日志文件路径（留空自动选择）",
	LogLevelPrompt: "日志级别（debug/info）",

	// --- Summary ---
	SummaryHeader:     "\n===========================================\n   配置摘要\n===========================================",
	SummaryDataDir:    "  数据目录:            %s\n",
	SummaryDefaultKB:  "  默认知识库:          %s\n",
	SummaryEmbedding:  "  Embedding:           %s\n",
	SummaryReranker:   "  Reranker:            %s\n",
	SummaryRerankLimit: "  Rerank 候选数:        %d\n",
	SummaryGPUSched:   "  GPU 调度器:          %s\n",
	SummaryManagePort: "  管理端口:            %s\n",
	SummaryLogLevel:   "  日志级别:            %s\n",
	SummaryEnabled:    "✓ 已启用",
	SummaryNotCfg:     "✗ 未配置",
	SummaryNone:       "(无)",
	SummaryEmbedSleep: "    - Embedding 睡眠:  %s\n",
	SummaryRerankSleep: "    - Reranker 睡眠:   %s\n",
	SummaryDocParser:   "  文档解析 API:       %s\n",
	SummaryDocParserSleep: "    - 文档解析睡眠:   %s\n",
}

var enStrs = langStr{
	// --- Banner ---
	Title:       "knowledge-mcp Interactive Setup Wizard",
	NavHint:     "Enter b to go back, q to quit at any step",
	BackCmd:     "b",
	QuitCmd:     "q",
	CancelMsg:   "\nConfiguration cancelled.",
	CompleteMsg: "\n✓ Setup complete. Knowledge base service is configured.",

	// --- Error / Confirm ---
	ErrorRetry: "Error: %v\nPress Enter to retry...",
	SavePrompt: "\nSave the above configuration? (Y/n): ",
	SaveFailed: "Failed to save config file: %v\n",
	SaveOK:     "✓ Configuration saved to %s\n",
	NoMCPJSON:  "(No .mcp.json found, skipping update)",

	// --- Data dir ---
	DataDirTitle:  "\n--- Data Directory ---",
	DataDirPrompt: "Knowledge base data storage directory",

	// --- Default KB ---
	DefaultKBTitle:  "\n--- Default Knowledge Base ---",
	DefaultKBHelp:   "(Optional, press Enter to skip. If set, AI tools use this KB by default)",
	DefaultKBPrompt: "Default knowledge base name",

	// --- Embedder ---
	EmbedderTitle:  "\n--- Embedding Model Configuration ---",
	EmbedderDesc:   "Converts documents to vectors. Supports any OpenAI-compatible API (e.g. Ollama)",
	EmbedderEnable: "Configure embedding model?",
	EmbedderURL:    "Embedding API endpoint",
	EmbedderModel:  "Embedding model name",
	EmbedderDim:    "Vector dimension (Enter for auto-detect)",
	EmbedderAPIKey: "API Key (optional, press Enter to skip)",
	EmbedTestTitle: "[Test] Testing embedding API connection...",
	EmbedTestOK:    "✓ Embedder connected successfully (dim=%d)\n",
	EmbedTestFail:  "✗ Connection failed: %v\n",
	EmbedRetry:     "(r) retry (s) skip (b) back: ",
	EmbedSkipped:   "Embedding configuration skipped",
	EmbedTestConn:  "Embedding API connection test",

	// --- Reranker ---
	RerankerTitle:  "\n--- Reranker Model Configuration ---",
	RerankerDesc:   "Improves search precision with second-pass ranking. Supports Infinity or Cohere-compatible API",
	RerankerEnable: "Configure reranker model?",
	RerankerURL:    "Reranker API endpoint",
	RerankerModel:  "Reranker model name",
	RerankerAPIKey: "API Key (optional, press Enter to skip)",
	RerankerTimeout: "Request timeout (e.g. 30s, 60s)",
	RerankTestTitle: "[Test] Testing reranker API connection...",
	RerankTestOK:    "✓ Reranker connected successfully\n",
	RerankTestFail:  "✗ Connection failed: %v\n",
	RerankRetry:     "(r) retry (s) skip (b) back: ",
	RerankSkipped:   "Reranker configuration skipped",
	RerankTestConn:  "Reranker API connection test",

	// --- Rerank limit ---
	RerankLimitTitle:  "\n--- Rerank Candidate Limit ---",
	RerankLimitDesc:   "Maximum number of candidate documents per reranking pass",
	RerankLimitPrompt: "Candidate limit",

	// --- Doc Parser ---
	DocParserTitle:  "\n--- Document Parser API ---",
	DocParserDesc:   "Parses non-plain-text documents (PDF, DOCX, etc.). External HTTP API is tried first; falls back to local tabula when unavailable",
	DocParserEnable: "Configure document parser API?",
	DocParserURL:    "Document parser API endpoint",
	DocParserAPIKey: "API Key (optional, press Enter to skip)",
	DocParserTimeout: "Request timeout (e.g. 30s, 120s)",

	// --- GPU Scheduler ---
	GPUSchedTitle:    "\n--- GPU Scheduler ---",
	GPUSchedDesc:     "Coordinates sleep/wake of embedding and reranker models sharing the same GPU",
	GPUSchedURLHint:  "Provide sleep/wake API URLs for both models to switch them in GPU memory",
	GPUSchedNoModels: "⚠ No models configured. GPU scheduler requires both embedding and reranker.\n   Please configure both models first before enabling the scheduler.",
	GPUSchedEnable:   "Enable GPU scheduler?",
	GPUSchedEmbedTitle: "\n--- Embedding Model Sleep API ---",
	GPUSchedEmbedSleep: "Embedding sleep API URL",
	GPUSchedRerankTitle: "\n--- Reranker Model Sleep API ---",
	GPUSchedRerankSleep: "Reranker sleep API URL",
	GPUSchedDocParserTitle: "\n--- Document Parser Model Sleep API ---",
	GPUSchedDocParserSleep: "Doc parser sleep API URL",
	GPUSchedTimeout:     "Scheduler request timeout",

	// --- Manage port ---
	ManagePortTitle:  "\n--- Management UI Port ---",
	ManagePortDesc:   "Port for the knowledge base management UI",
	ManagePortPrompt: "Management port",

	// --- Logging ---
	LogTitle:       "\n--- Logging Configuration ---",
	LogFilePath:    "Log file path (Enter for auto)",
	LogLevelPrompt: "Log level (debug/info)",

	// --- Summary ---
	SummaryHeader:     "\n===========================================\n   Configuration Summary\n===========================================",
	SummaryDataDir:    "  Data Directory:       %s\n",
	SummaryDefaultKB:  "  Default KB:           %s\n",
	SummaryEmbedding:  "  Embedding:            %s\n",
	SummaryReranker:   "  Reranker:             %s\n",
	SummaryRerankLimit: "  Rerank Candidate Limit: %d\n",
	SummaryGPUSched:   "  GPU Scheduler:        %s\n",
	SummaryManagePort: "  Management Port:      %s\n",
	SummaryLogLevel:   "  Log Level:            %s\n",
	SummaryEnabled:    "✓ Enabled",
	SummaryNotCfg:     "✗ Not configured",
	SummaryNone:       "(none)",
	SummaryEmbedSleep: "    - Embedding Sleep:  %s\n",
	SummaryRerankSleep: "    - Reranker Sleep:   %s\n",
	SummaryDocParser:   "  Doc Parser API:       %s\n",
	SummaryDocParserSleep: "    - Doc Parser Sleep: %s\n",
}

// currentLang is the language selected by the user at the start of the wizard.
// Default is Chinese.
var currentLang Lang = LangZH

// setLang switches the wizard language.
func setLang(l Lang) {
	currentLang = l
}

// T returns the language strings for the current language.
func T() *langStr {
	if currentLang == LangEN {
		return &enStrs
	}
	return &zhStrs
}
