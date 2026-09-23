package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/MACOS-DO/sub4api/internal/config"
	"github.com/MACOS-DO/sub4api/internal/repository"
	"github.com/MACOS-DO/sub4api/internal/service"
)

type modelTraceResult struct {
	Prediction  string  `json:"prediction"`
	Probability float64 `json:"probability"`
	UsedOutputs int     `json:"used_outputs"`
	Family      string  `json:"family_prediction"`
	Error       string  `json:"error"`
	Diagnostics []struct {
		ParsedNumbers  int `json:"parsed_numbers"`
		MinimumNumbers int `json:"minimum_numbers"`
	} `json:"diagnostics"`
}

func main() {
	envPath := flag.String("env", "cmd/codex-ticket-hypothesis/.env", "dedicated .env file")
	sessionPath := flag.String("session-jsonl", "", "optional Codex Desktop session record for initial-context replay")
	duration := flag.Duration("watch-duration", time.Hour, "maximum elapsed time, including ticket search")
	flag.Parse()
	if *duration <= 0 || *duration > time.Hour {
		fmt.Fprintln(os.Stderr, "watch-duration must be between 1ns and 1h")
		os.Exit(1)
	}
	if err := runLoggedWithReplay(*envPath, *sessionPath, *duration, os.Stdout); err != nil {
		os.Exit(1)
	}
}

func run(ctx context.Context, started time.Time, envPath, sessionPath string, duration time.Duration, output io.Writer) error {
	values, err := readEnvFile(envPath)
	if err != nil {
		return err
	}
	options := service.CodexHypothesisOptions{
		Token: values["ACCESS_TOKEN"], ChatGPTAccountID: values["CHATGPT_ACCOUNT_ID"], Model: values["MODEL"], ProxyURL: values["PROXY_URL"],
	}
	if sessionPath != "" {
		options.Replay, err = loadSessionReplay(sessionPath, envPath)
		if err != nil {
			return err
		}
		fmt.Fprintf(output, "重放上下文：Codex Desktop 初始消息 | 时区：Asia/Singapore | 原记录模型：%s | 测试模型：%s\n", options.Replay.RecordedModel, options.Model)
		if options.Replay.RecordedModel != options.Model {
			fmt.Fprintln(output, "⚠ 原记录的模型自述与测试模型不同，结果可能受上下文影响")
		}
	}
	runner, err := service.NewCodexHypothesisRunner(repository.NewHTTPUpstream(&config.Config{}), options)
	if err != nil {
		return err
	}
	fmt.Fprintln(output, "本地预检：检查 Node、ModelTrace 指纹库及单条挑战生成（不访问上游）")
	first, err := generateChallenges(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "预检完成 | 期望模型：%s | 最长运行：%s | 找票/验票并发：3 | 各工位固定等待：100ms\n", options.Model, duration)
	firstReady := true
	var firstMu sync.Mutex
	return watch(ctx, options.Model, output, watchDependencies{
		started:  started,
		proxyURL: options.ProxyURL,
		challenge: func(ctx context.Context) (service.CodexHypothesisChallenge, error) {
			firstMu.Lock()
			if firstReady {
				firstReady = false
				firstMu.Unlock()
				return first[0], nil
			}
			firstMu.Unlock()
			items, err := generateChallenges(ctx)
			if err != nil {
				return service.CodexHypothesisChallenge{}, err
			}
			return items[0], nil
		},
		probe: runner.RunChallenge,
		score: func(ctx context.Context, output service.CodexHypothesisOutput) (modelTraceResult, error) {
			return scoreOutputs(ctx, []service.CodexHypothesisOutput{output})
		},
		wait: waitWithHeartbeat,
	})
}

func readEnvFile(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open dedicated .env: %w", err)
	}
	defer file.Close()
	values := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(name) == "" {
			return nil, errors.New("invalid .env entry")
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		values[strings.TrimSpace(name)] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read dedicated .env: %w", err)
	}
	return values, nil
}

func generateChallenges(ctx context.Context) ([]service.CodexHypothesisChallenge, error) {
	payload, err := runModelTrace(ctx, "challenges", nil)
	if err != nil {
		return nil, err
	}
	var result struct {
		Challenges []service.CodexHypothesisChallenge `json:"challenges"`
	}
	if err := json.Unmarshal(payload, &result); err != nil {
		return nil, fmt.Errorf("decode ModelTrace challenges: %w", err)
	}
	if len(result.Challenges) != 1 {
		return nil, errors.New("ModelTrace did not generate one challenge")
	}
	return result.Challenges, nil
}

func scoreOutputs(ctx context.Context, outputs []service.CodexHypothesisOutput) (modelTraceResult, error) {
	var result modelTraceResult
	if len(outputs) != 1 {
		return result, errors.New("ModelTrace requires one challenge output")
	}
	payload, err := json.Marshal(map[string]any{"outputs": outputs})
	if err != nil {
		return result, err
	}
	encoded, err := runModelTrace(ctx, "analyze", payload)
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal(encoded, &result); err != nil {
		return result, fmt.Errorf("decode ModelTrace result: %w", err)
	}
	if result.UsedOutputs != 1 || result.Prediction == "" {
		counts := make([]string, 0, len(result.Diagnostics))
		for _, item := range result.Diagnostics {
			counts = append(counts, fmt.Sprintf("%d/%d", item.ParsedNumbers, item.MinimumNumbers))
		}
		return result, fmt.Errorf("ModelTrace valid outputs %d/1 (parsed/required: %s): %s", result.UsedOutputs, strings.Join(counts, ", "), result.Error)
	}
	return result, nil
}

func runModelTrace(ctx context.Context, mode string, input []byte) ([]byte, error) {
	node, err := exec.LookPath("node")
	if err != nil {
		return nil, errors.New("Node is required for local ModelTrace scoring")
	}
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		return nil, errors.New("cannot locate bundled ModelTrace assets")
	}
	bridge := filepath.Join(filepath.Dir(sourceFile), "modeltrace", "runner.mjs")
	if _, err := os.Stat(bridge); err != nil {
		return nil, fmt.Errorf("bundled ModelTrace bridge unavailable: %w", err)
	}
	commandCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	command := exec.CommandContext(commandCtx, node, bridge, mode)
	command.Env = []string{"PATH=" + os.Getenv("PATH")}
	command.Stdin = bytes.NewReader(input)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		message := stderr.String()
		if len(message) > 200 {
			message = message[:200]
		}
		return nil, fmt.Errorf("local ModelTrace %s failed: %w: %s", mode, err, strings.TrimSpace(message))
	}
	return output, nil
}

func printPhase(output io.Writer, name string, phase service.CodexHypothesisPhase, score modelTraceResult) {
	fmt.Fprintf(output, "%s：HTTP=%v，ModelTrace top-1=%s (%.2f%%)，family=%s，使用=%d/3\n",
		name, phase.Statuses, score.Prediction, score.Probability*100, score.Family, score.UsedOutputs)
	if name == "无票" {
		encoded, _ := json.MarshalIndent(phase.Headers, "  ", "  ")
		fmt.Fprintf(output, "脱敏出站请求头：%s\n", encoded)
	}
}
