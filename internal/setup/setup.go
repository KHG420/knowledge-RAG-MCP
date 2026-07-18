// Package setup provides the interactive "knowledge-mcp setup" CLI command
// for configuring the knowledge-mcp server.
package setup

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"knowledge-mcp/internal/config"
	"knowledge-mcp/internal/knowledge"
)

// Shared scanner for all steps — created once in Run() so buffered input
// isn't lost between steps.
var stdIn *bufio.Scanner

// step represents one interactive configuration step.
type step struct {
	name string
	run  func(*config.Config) error
}

// Run starts the interactive setup wizard.
func Run() {
	fmt.Println("===========================================")
	fmt.Println("   knowledge-mcp 交互式配置向导")
	fmt.Println("===========================================")
	fmt.Println("每步可输入 b 返回上一步，输入 q 退出")
	fmt.Println()

	cfg := config.DefaultConfig()
	stdIn = bufio.NewScanner(os.Stdin)

	steps := []step{
		{name: "data_dir", run: stepDataDir},
		{name: "default_kb", run: stepDefaultKB},
		{name: "embedder", run: stepEmbedder},
		{name: "reranker", run: stepReranker},
		{name: "rerank_limit", run: stepRerankLimit},
		{name: "gpu_scheduler", run: stepGPUScheduler},
		{name: "manage_port", run: stepManagePort},
		{name: "logging", run: stepLogging},
	}

	current := 0
	for current >= 0 && current < len(steps) {
		s := steps[current]
		result := s.run(cfg)
		switch {
		case result == nil:
			current++
		case result == errBack:
			current--
		case result == errQuit:
			fmt.Println("\n配置已取消。")
			return
		default:
			fmt.Printf("错误: %v\n", result)
			fmt.Print("按 Enter 重试...")
			stdIn.Scan()
		}
	}

	// Summary and confirmation.
	showSummary(cfg)
	if !confirmSave() {
		fmt.Println("配置已取消。")
		return
	}

	// Determine save path.
	savePath := findSetupConfigPath()
	if err := config.Save(savePath, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "保存配置文件失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ 配置已保存到 %s\n", savePath)

	// Optionally update .mcp.json.
	updateMCPJSON()

	fmt.Println("\n✓ Setup complete. 知识库服务已配置完毕。")
}

var errBack = fmt.Errorf("back")
var errQuit = fmt.Errorf("quit")

func readLine() string {
	if !stdIn.Scan() {
		return ""
	}
	return strings.TrimSpace(stdIn.Text())
}

func prompt(text, defaultVal string) string {
	if defaultVal != "" {
		fmt.Printf("%s [%s]: ", text, defaultVal)
	} else {
		fmt.Printf("%s: ", text)
	}
	input := readLine()
	switch strings.ToLower(input) {
	case "b", "back":
		return "__BACK__"
	case "q", "quit", "exit":
		return "__QUIT__"
	}
	if input == "" {
		return defaultVal
	}
	return input
}

// promptYN returns "yes", "no", or "back".
func promptYN(text string, defaultVal bool) string {
	def := "n"
	if defaultVal {
		def = "y"
	}
	fmt.Printf("%s (y/n) [%s]: ", text, def)
	input := strings.ToLower(readLine())
	switch input {
	case "b", "back":
		return "back"
	case "y", "yes":
		return "yes"
	default:
		if defaultVal {
			return "yes"
		}
		return "no"
	}
}

func checkBackQuit(val string) error {
	switch val {
	case "__BACK__":
		return errBack
	case "__QUIT__":
		return errQuit
	}
	return nil
}

// --- Step implementations ---

func stepDataDir(cfg *config.Config) error {
	fmt.Println("\n--- 数据目录 ---")
	val := prompt("知识库数据存储目录", "~/knowledge_base/")
	if err := checkBackQuit(val); err != nil {
		return err
	}
	cfg.DataDir = val
	return nil
}

func stepDefaultKB(cfg *config.Config) error {
	fmt.Println("\n--- 默认知识库 ---")
	fmt.Println("(可选，回车跳过。如设置，AI 工具调用时默认使用此知识库)")
	val := prompt("默认知识库名称", "")
	if err := checkBackQuit(val); err != nil {
		return err
	}
	cfg.DefaultKB = val
	return nil
}

func stepEmbedder(cfg *config.Config) error {
	fmt.Println("\n--- Embedding 模型配置 ---")
	fmt.Println("用于将文档转为向量，支持任何 OpenAI 兼容 API（如 Ollama）")

	enable := promptYN("是否配置 embedding 模型", false)
	switch enable {
	case "back":
		return errBack
	case "no":
		return nil
	}

	val := prompt("Embedding API 地址", "http://localhost:11434/v1/embeddings")
	if err := checkBackQuit(val); err != nil {
		return err
	}
	cfg.EmbedEndpoint = val

	val = prompt("Embedding 模型名称", "bge-m3")
	if err := checkBackQuit(val); err != nil {
		return err
	}
	cfg.EmbedModel = val

	val = prompt("向量维度（回车=自动检测）", "")
	if err := checkBackQuit(val); err != nil {
		return err
	}
	if val != "" {
		if n, err := strconv.Atoi(val); err == nil && n > 0 {
			cfg.EmbedDim = n
		}
	}

	val = prompt("API Key（可选，回车跳过）", "")
	if err := checkBackQuit(val); err != nil {
		return err
	}
	cfg.EmbedAPIKey = val

	// Test connection
	return testEmbedder(cfg)
}

func stepReranker(cfg *config.Config) error {
	fmt.Println("\n--- Reranker 模型配置 ---")
	fmt.Println("用于二次排序提高搜索精度。支持 Infinity 或 Cohere 兼容 API")

	enable := promptYN("是否配置 reranker 模型", false)
	switch enable {
	case "back":
		return errBack
	case "no":
		return nil
	}

	val := prompt("Reranker API 地址", "http://localhost:7997/rerank")
	if err := checkBackQuit(val); err != nil {
		return err
	}
	cfg.RerankEndpoint = val

	val = prompt("Reranker 模型名称", "gte-multilingual-reranker-base")
	if err := checkBackQuit(val); err != nil {
		return err
	}
	cfg.RerankModel = val

	val = prompt("API Key（可选，回车跳过）", "")
	if err := checkBackQuit(val); err != nil {
		return err
	}
	cfg.RerankAPIKey = val

	val = prompt("请求超时时间（如 30s, 60s）", "30s")
	if err := checkBackQuit(val); err != nil {
		return err
	}
	cfg.RerankTimeout = val

	// Test connection
	return testReranker(cfg)
}

func stepRerankLimit(cfg *config.Config) error {
	fmt.Println("\n--- Rerank 候选数限制 ---")
	fmt.Println("每次重排序最多处理多少个候选文档")

	val := prompt("候选数", "100")
	if err := checkBackQuit(val); err != nil {
		return err
	}
	if n, err := strconv.Atoi(val); err == nil && n > 0 {
		cfg.RerankCandidateLimit = n
	}
	return nil
}

func stepGPUScheduler(cfg *config.Config) error {
	fmt.Println("\n--- GPU 调度器 ---")
	fmt.Println("当 embedding 和 reranker 模型共享 GPU 时，调度器协调两者的睡眠/唤醒")

	enable := promptYN("是否启用 GPU 调度器", false)
	switch enable {
	case "back":
		return errBack
	case "no":
		return nil
	}
	cfg.GPUSchedulerEnabled = true

	val := prompt("调度请求超时时间", "30s")
	if err := checkBackQuit(val); err != nil {
		return err
	}
	cfg.GPUSchedulerTimeout = val

	val = prompt("唤醒后等待模型加载时间", "3s")
	if err := checkBackQuit(val); err != nil {
		return err
	}
	cfg.GPUSchedulerWakeDelay = val
	return nil
}

func stepManagePort(cfg *config.Config) error {
	fmt.Println("\n--- 管理界面端口 ---")
	fmt.Println("知识库的管理 UI 监听端口")

	val := prompt("管理端口", "8085")
	if err := checkBackQuit(val); err != nil {
		return err
	}
	cfg.ManagePort = val
	return nil
}

func stepLogging(cfg *config.Config) error {
	fmt.Println("\n--- 日志配置 ---")

	exeDir := ""
	if exe, err := os.Executable(); err == nil {
		exeDir = filepath.Dir(exe)
	}
	defaultLogFile := ""
	if exeDir != "" {
		defaultLogFile = filepath.Join(exeDir, "knowledge-mcp.log")
	}

	val := prompt("日志文件路径（留空自动选择）", defaultLogFile)
	if err := checkBackQuit(val); err != nil {
		return err
	}
	cfg.LogFile = val

	val = prompt("日志级别（debug/info）", "info")
	if err := checkBackQuit(val); err != nil {
		return err
	}
	cfg.LogLevel = val
	return nil
}

// --- Testing ---

func testEmbedder(cfg *config.Config) error {
	fmt.Println("\n[测试] 正在测试 embedding API 连接...")
	opts := []knowledge.OpenAIEmbedderOption{
		knowledge.WithEndpointURL(cfg.EmbedEndpoint),
		knowledge.WithModel(cfg.EmbedModel),
	}
	if cfg.EmbedDim > 0 {
		opts = append(opts, knowledge.WithDim(cfg.EmbedDim))
	}
	if cfg.EmbedAPIKey != "" {
		opts = append(opts, knowledge.WithAPIKey(cfg.EmbedAPIKey))
	}
	embedder := knowledge.NewOpenAIEmbedder(opts...)

	if err := embedder.Probe(context.Background()); err != nil {
		fmt.Printf("✗ 连接失败: %v\n", err)
		fmt.Print("(r) 重试 (s) 跳过 (b) 返回: ")
		input := strings.ToLower(readLine())
		switch input {
		case "r", "retry":
			return testEmbedder(cfg)
		case "s", "skip":
			fmt.Println("已跳过 embedding 配置")
			cfg.EmbedEndpoint = ""
			return nil
		case "b", "back":
			return errBack
		default:
			fmt.Println("已跳过 embedding 配置")
			cfg.EmbedEndpoint = ""
			return nil
		}
	}
	fmt.Printf("✓ Embedder 连接成功 (dim=%d)\n", embedder.Dim())
	cfg.EmbedDim = embedder.Dim()
	return nil
}

func testReranker(cfg *config.Config) error {
	fmt.Println("\n[测试] 正在测试 reranker API 连接...")
	opts := []knowledge.InfinityRerankerOption{
		knowledge.WithRerankEndpointURL(cfg.RerankEndpoint),
		knowledge.WithRerankModel(cfg.RerankModel),
	}
	if cfg.RerankAPIKey != "" {
		opts = append(opts, knowledge.WithRerankAPIKey(cfg.RerankAPIKey))
	}
	if cfg.RerankTimeout != "" {
		if d, err := time.ParseDuration(cfg.RerankTimeout); err == nil {
			opts = append(opts, knowledge.WithRerankTimeout(d))
		}
	}
	reranker := knowledge.NewInfinityReranker(opts...)

	if err := reranker.Probe(context.Background()); err != nil {
		fmt.Printf("✗ 连接失败: %v\n", err)
		fmt.Print("(r) 重试 (s) 跳过 (b) 返回: ")
		input := strings.ToLower(readLine())
		switch input {
		case "r", "retry":
			return testReranker(cfg)
		case "s", "skip":
			fmt.Println("已跳过 reranker 配置")
			cfg.RerankEndpoint = ""
			return nil
		case "b", "back":
			return errBack
		default:
			fmt.Println("已跳过 reranker 配置")
			cfg.RerankEndpoint = ""
			return nil
		}
	}
	fmt.Println("✓ Reranker 连接成功")
	return nil
}

// --- Summary & Save ---

func showSummary(cfg *config.Config) {
	fmt.Println("\n===========================================")
	fmt.Println("   配置摘要")
	fmt.Println("===========================================")
	fmt.Printf("  数据目录:            %s\n", cfg.DataDir)
	fmt.Printf("  默认知识库:          %s\n", orNone(cfg.DefaultKB))
	fmt.Printf("  Embedding:           %s\n", onOff(cfg.EmbedEndpoint != ""))
	if cfg.EmbedEndpoint != "" {
		fmt.Printf("    - 地址:           %s\n", cfg.EmbedEndpoint)
		fmt.Printf("    - 模型:           %s\n", cfg.EmbedModel)
		fmt.Printf("    - 维度:           %d\n", cfg.EmbedDim)
	}
	fmt.Printf("  Reranker:            %s\n", onOff(cfg.RerankEndpoint != ""))
	if cfg.RerankEndpoint != "" {
		fmt.Printf("    - 地址:           %s\n", cfg.RerankEndpoint)
		fmt.Printf("    - 模型:           %s\n", cfg.RerankModel)
	}
	fmt.Printf("  Rerank 候选数:        %d\n", cfg.RerankCandidateLimit)
	fmt.Printf("  GPU 调度器:          %s\n", onOff(cfg.GPUSchedulerEnabled))
	fmt.Printf("  管理端口:            %s\n", cfg.ManagePort)
	fmt.Printf("  日志级别:            %s\n", cfg.LogLevel)
	fmt.Println("===========================================")
}

func onOff(enabled bool) string {
	if enabled {
		return "✓ 已启用"
	}
	return "✗ 未配置"
}

func orNone(s string) string {
	if s == "" {
		return "(无)"
	}
	return s
}

func confirmSave() bool {
	fmt.Print("\n确认保存以上配置？(Y/n): ")
	input := strings.ToLower(readLine())
	return input != "n" && input != "no"
}

func findSetupConfigPath() string {
	if exe, err := os.Executable(); err == nil {
		return filepath.Join(filepath.Dir(exe), "knowledge-mcp.toml")
	}
	return "knowledge-mcp.toml"
}

func updateMCPJSON() {
	candidates := []string{".mcp.json"}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), ".mcp.json"))
	}

	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var mcpCfg struct {
			MCPServers map[string]any `json:"mcpServers"`
		}
		if err := json.Unmarshal(data, &mcpCfg); err != nil {
			continue
		}
		entry, ok := mcpCfg.MCPServers["knowledge-mcp"]
		if !ok {
			continue
		}
		entryMap, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		delete(entryMap, "env")
		mcpCfg.MCPServers["knowledge-mcp"] = entryMap
		newData, err := json.MarshalIndent(mcpCfg, "", "  ")
		if err != nil {
			continue
		}
		if err := os.WriteFile(path, newData, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "警告: 无法更新 %s: %v\n", path, err)
			continue
		}
		fmt.Printf("✓ 已更新 %s（移除了 env 字段）\n", path)
		return
	}
	fmt.Println("(未找到 .mcp.json，跳过更新)")
}
