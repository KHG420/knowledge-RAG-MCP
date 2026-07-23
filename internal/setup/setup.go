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
	// Language selection.
	fmt.Println("===========================================")
	fmt.Println("  knowledge-mcp Setup Wizard")
	fmt.Println("===========================================")
	fmt.Println()
	fmt.Println("Select language / 选择语言:")
	fmt.Println("  1) 中文")
	fmt.Println("  2) English")
	fmt.Print("Enter 1 or 2 [1]: ")
	stdIn = bufio.NewScanner(os.Stdin)
	if stdIn.Scan() {
		ch := strings.TrimSpace(stdIn.Text())
		if ch == "2" {
			setLang(LangEN)
		}
	}
	fmt.Println()

	lt := T()
	fmt.Println("===========================================")
	fmt.Printf("   %s\n", lt.Title)
	fmt.Println("===========================================")
	fmt.Printf("%s （%s=%s %s=%s）\n", lt.NavHint, lt.BackCmd, lt.BackCmd, lt.QuitCmd, lt.QuitCmd)
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
			fmt.Println(lt.CancelMsg)
			return
		default:
			fmt.Printf(lt.ErrorRetry, result)
			stdIn.Scan()
		}
	}

	// Summary and confirmation.
	lt = T()
	showSummary(cfg)
	if !confirmSave() {
		fmt.Println(lt.CancelMsg)
		return
	}

	// Determine save path.
	savePath := findSetupConfigPath()
	if err := config.Save(savePath, cfg); err != nil {
		fmt.Fprintf(os.Stderr, lt.SaveFailed, err)
		os.Exit(1)
	}
	fmt.Printf(lt.SaveOK, savePath)

	// Optionally update .mcp.json.
	updateMCPJSON()

	fmt.Println(lt.CompleteMsg)
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
	lt := T()
	fmt.Println(lt.DataDirTitle)
	val := prompt(lt.DataDirPrompt, "~/knowledge_base/")
	if err := checkBackQuit(val); err != nil {
		return err
	}
	cfg.DataDir = val
	return nil
}

func stepDefaultKB(cfg *config.Config) error {
	lt := T()
	fmt.Println(lt.DefaultKBTitle)
	fmt.Println(lt.DefaultKBHelp)
	val := prompt(lt.DefaultKBPrompt, "")
	if err := checkBackQuit(val); err != nil {
		return err
	}
	cfg.DefaultKB = val
	return nil
}

func stepEmbedder(cfg *config.Config) error {
	lt := T()
	fmt.Println(lt.EmbedderTitle)
	fmt.Println(lt.EmbedderDesc)

	enable := promptYN(lt.EmbedderEnable, false)
	switch enable {
	case "back":
		return errBack
	case "no":
		return nil
	}

	val := prompt(lt.EmbedderURL, "http://localhost:11434/v1/embeddings")
	if err := checkBackQuit(val); err != nil {
		return err
	}
	cfg.EmbedEndpoint = val

	val = prompt(lt.EmbedderModel, "bge-m3")
	if err := checkBackQuit(val); err != nil {
		return err
	}
	cfg.EmbedModel = val

	val = prompt(lt.EmbedderDim, "")
	if err := checkBackQuit(val); err != nil {
		return err
	}
	if val != "" {
		if n, err := strconv.Atoi(val); err == nil && n > 0 {
			cfg.EmbedDim = n
		}
	}

	val = prompt(lt.EmbedderAPIKey, "")
	if err := checkBackQuit(val); err != nil {
		return err
	}
	cfg.EmbedAPIKey = val

	// Test connection
	return testEmbedder(cfg)
}

func stepReranker(cfg *config.Config) error {
	lt := T()
	fmt.Println(lt.RerankerTitle)
	fmt.Println(lt.RerankerDesc)

	enable := promptYN(lt.RerankerEnable, false)
	switch enable {
	case "back":
		return errBack
	case "no":
		return nil
	}

	val := prompt(lt.RerankerURL, "http://localhost:7997/rerank")
	if err := checkBackQuit(val); err != nil {
		return err
	}
	cfg.RerankEndpoint = val

	val = prompt(lt.RerankerModel, "gte-multilingual-reranker-base")
	if err := checkBackQuit(val); err != nil {
		return err
	}
	cfg.RerankModel = val

	val = prompt(lt.RerankerAPIKey, "")
	if err := checkBackQuit(val); err != nil {
		return err
	}
	cfg.RerankAPIKey = val

	val = prompt(lt.RerankerTimeout, "30s")
	if err := checkBackQuit(val); err != nil {
		return err
	}
	cfg.RerankTimeout = val

	// Test connection
	return testReranker(cfg)
}

func stepRerankLimit(cfg *config.Config) error {
	lt := T()
	fmt.Println(lt.RerankLimitTitle)
	fmt.Println(lt.RerankLimitDesc)

	val := prompt(lt.RerankLimitPrompt, "100")
	if err := checkBackQuit(val); err != nil {
		return err
	}
	if n, err := strconv.Atoi(val); err == nil && n > 0 {
		cfg.RerankCandidateLimit = n
	}
	return nil
}

func stepGPUScheduler(cfg *config.Config) error {
	lt := T()
	fmt.Println(lt.GPUSchedTitle)
	fmt.Println(lt.GPUSchedDesc)
	fmt.Println(lt.GPUSchedURLHint)

	if cfg.EmbedEndpoint == "" && cfg.RerankEndpoint == "" {
		fmt.Println(lt.GPUSchedNoModels)
	}

	enable := promptYN(lt.GPUSchedEnable, false)
	switch enable {
	case "back":
		return errBack
	case "no":
		return nil
	}
	cfg.GPUSchedulerEnabled = true

	// Embedding model sleep URL (only if embedder is configured).
	if cfg.EmbedEndpoint != "" {
		fmt.Println(lt.GPUSchedEmbedTitle)
		defaultSleepURL := cfg.EmbedEndpoint
		if strings.HasSuffix(defaultSleepURL, "/embeddings") {
			defaultSleepURL = strings.TrimSuffix(defaultSleepURL, "/embeddings") + "/sleep"
		}

		val := prompt(lt.GPUSchedEmbedSleep, defaultSleepURL)
		if err := checkBackQuit(val); err != nil {
			return err
		}
		cfg.GPUSchedulerEmbeddingSleepURL = val
	}

	// Reranker model sleep URL (only if reranker is configured).
	if cfg.RerankEndpoint != "" {
		fmt.Println(lt.GPUSchedRerankTitle)
		baseURL := cfg.RerankEndpoint
		if idx := strings.LastIndex(baseURL, "/rerank"); idx > 0 {
			baseURL = baseURL[:idx]
		}

		val := prompt(lt.GPUSchedRerankSleep, baseURL+"/sleep")
		if err := checkBackQuit(val); err != nil {
			return err
		}
		cfg.GPUSchedulerRerankerSleepURL = val
	}

	val := prompt(lt.GPUSchedTimeout, "30s")
	if err := checkBackQuit(val); err != nil {
		return err
	}
	cfg.GPUSchedulerTimeout = val
	return nil
}

func stepManagePort(cfg *config.Config) error {
	lt := T()
	fmt.Println(lt.ManagePortTitle)
	fmt.Println(lt.ManagePortDesc)

	val := prompt(lt.ManagePortPrompt, "8085")
	if err := checkBackQuit(val); err != nil {
		return err
	}
	cfg.ManagePort = val
	return nil
}

func stepLogging(cfg *config.Config) error {
	lt := T()
	fmt.Println(lt.LogTitle)

	exeDir := ""
	if exe, err := os.Executable(); err == nil {
		exeDir = filepath.Dir(exe)
	}
	defaultLogFile := ""
	if home, err := os.UserHomeDir(); err == nil {
		defaultLogFile = filepath.Join(home, ".knowledge-mcp", "knowledge-mcp.log")
	} else if exeDir != "" {
		defaultLogFile = filepath.Join(exeDir, "knowledge-mcp.log")
	}

	val := prompt(lt.LogFilePath, defaultLogFile)
	if err := checkBackQuit(val); err != nil {
		return err
	}
	cfg.LogFile = val

	val = prompt(lt.LogLevelPrompt, "info")
	if err := checkBackQuit(val); err != nil {
		return err
	}
	cfg.LogLevel = val
	return nil
}

// --- Testing ---

func testEmbedder(cfg *config.Config) error {
	lt := T()
	fmt.Println(lt.EmbedTestTitle)
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
		fmt.Printf(lt.EmbedTestFail, err)
		fmt.Print(lt.EmbedRetry)
		input := strings.ToLower(readLine())
		switch input {
		case "r", "retry":
			return testEmbedder(cfg)
		case "s", "skip":
			fmt.Println(lt.EmbedSkipped)
			cfg.EmbedEndpoint = ""
			return nil
		case "b", "back":
			return errBack
		default:
			fmt.Println(lt.EmbedSkipped)
			cfg.EmbedEndpoint = ""
			return nil
		}
	}
	fmt.Printf(lt.EmbedTestOK, embedder.Dim())
	cfg.EmbedDim = embedder.Dim()
	return nil
}

func testReranker(cfg *config.Config) error {
	lt := T()
	fmt.Println(lt.RerankTestTitle)
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
		fmt.Printf(lt.RerankTestFail, err)
		fmt.Print(lt.RerankRetry)
		input := strings.ToLower(readLine())
		switch input {
		case "r", "retry":
			return testReranker(cfg)
		case "s", "skip":
			fmt.Println(lt.RerankSkipped)
			cfg.RerankEndpoint = ""
			return nil
		case "b", "back":
			return errBack
		default:
			fmt.Println(lt.RerankSkipped)
			cfg.RerankEndpoint = ""
			return nil
		}
	}
	fmt.Println(lt.RerankTestOK)
	return nil
}

// --- Summary & Save ---

func showSummary(cfg *config.Config) {
	lt := T()
	fmt.Println(lt.SummaryHeader)
	fmt.Printf(lt.SummaryDataDir, cfg.DataDir)
	fmt.Printf(lt.SummaryDefaultKB, orNone(cfg.DefaultKB))
	fmt.Printf(lt.SummaryEmbedding, onOff(cfg.EmbedEndpoint != ""))
	if cfg.EmbedEndpoint != "" {
		fmt.Printf("    - URL:            %s\n", cfg.EmbedEndpoint)
		fmt.Printf("    - Model:          %s\n", cfg.EmbedModel)
		fmt.Printf("    - Dim:            %d\n", cfg.EmbedDim)
	}
	fmt.Printf(lt.SummaryReranker, onOff(cfg.RerankEndpoint != ""))
	if cfg.RerankEndpoint != "" {
		fmt.Printf("    - URL:            %s\n", cfg.RerankEndpoint)
		fmt.Printf("    - Model:          %s\n", cfg.RerankModel)
	}
	fmt.Printf(lt.SummaryRerankLimit, cfg.RerankCandidateLimit)
	fmt.Printf(lt.SummaryGPUSched, onOff(cfg.GPUSchedulerEnabled))
	if cfg.GPUSchedulerEnabled {
		if cfg.GPUSchedulerEmbeddingSleepURL != "" {
			fmt.Printf(lt.SummaryEmbedSleep, cfg.GPUSchedulerEmbeddingSleepURL)
		}
		if cfg.GPUSchedulerRerankerSleepURL != "" {
			fmt.Printf(lt.SummaryRerankSleep, cfg.GPUSchedulerRerankerSleepURL)
		}
	}
	fmt.Printf(lt.SummaryManagePort, cfg.ManagePort)
	fmt.Printf(lt.SummaryLogLevel, cfg.LogLevel)
	fmt.Println("===========================================")
}

func onOff(enabled bool) string {
	lt := T()
	if enabled {
		return lt.SummaryEnabled
	}
	return lt.SummaryNotCfg
}

func orNone(s string) string {
	lt := T()
	if s == "" {
		return lt.SummaryNone
	}
	return s
}

func confirmSave() bool {
	fmt.Print(T().SavePrompt)
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
			fmt.Fprintf(os.Stderr, "warning: failed to update %s: %v\n", path, err)
			continue
		}
		fmt.Printf("✓ Updated %s (removed env field)\n", path)
		return
	}
	fmt.Println(T().NoMCPJSON)
}
