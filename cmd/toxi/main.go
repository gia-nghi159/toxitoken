package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"toxitoken/pkg/models"
)

var rootCmd = &cobra.Command{
	Use:   "toxi",
	Short: "Toxitoken Developer CLI - Edge proxy companion and testing tool",
}

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Configure target proxy gateway URL and API token",
	RunE: func(cmd *cobra.Command, args []string) error {
		urlFlag, _ := cmd.Flags().GetString("url")
		tokenFlag, _ := cmd.Flags().GetString("token")
		modeFlag, _ := cmd.Flags().GetString("mode")
		chaosFlag, _ := cmd.Flags().GetString("chaos-config")

		cfg, err := loadConfig()
		if err != nil {
			cfg = &CLIConfig{Mode: "live"}
		}

		if cmd.Flags().Changed("url") {
			cfg.GatewayURL = strings.TrimRight(urlFlag, "/")
		} else if cfg.GatewayURL == "" {
			cfg.GatewayURL = "http://localhost:8080"
		}

		if cmd.Flags().Changed("token") {
			cfg.Token = tokenFlag
		}

		if cmd.Flags().Changed("mode") {
			mode := strings.ToLower(modeFlag)
			if mode != "live" && mode != "mock" && mode != "chaos" {
				return fmt.Errorf("invalid mode: %s (must be live, mock, or chaos)", mode)
			}
			cfg.Mode = mode
		}

		if cmd.Flags().Changed("chaos-config") {
			cfg.ChaosConfig = chaosFlag
		}

		if err := saveConfig(cfg); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}

		fmt.Printf("✓ Config saved to ~/.toxi/config.json\n  Gateway: %s\n  Mode: %s\n", cfg.GatewayURL, cfg.Mode)
		return nil
	},
}

var modeCmd = &cobra.Command{
	Use:   "mode [live|mock|chaos]",
	Short: "Switch active gateway operating mode",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		mode := strings.ToLower(args[0])
		if mode != "live" && mode != "mock" && mode != "chaos" {
			return fmt.Errorf("invalid mode: %s (must be live, mock, or chaos)", mode)
		}

		cfg, err := loadConfig()
		if err != nil {
			return err
		}

		cfg.Mode = mode
		if cmd.Flags().Changed("chaos-config") {
			chaosFlag, _ := cmd.Flags().GetString("chaos-config")
			cfg.ChaosConfig = chaosFlag
		}

		if err := saveConfig(cfg); err != nil {
			return err
		}

		fmt.Printf("✓ Toxitoken mode set to [%s]\n", mode)
		return nil
	},
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show current configuration",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return fmt.Errorf("failed to load config: %w", err)
		}
		
		maskedToken := "<none>"
		if len(cfg.Token) > 4 {
			maskedToken = cfg.Token[:4] + "****"
		} else if cfg.Token != "" {
			maskedToken = "****"
		}

		fmt.Printf("Gateway: %s\nMode:    %s\n", cfg.GatewayURL, cfg.Mode)
		if cfg.ChaosConfig != "" {
			fmt.Printf("Chaos:   %s\n", cfg.ChaosConfig)
		}
		fmt.Printf("Token:   %s\n", maskedToken)
		
		return nil
	},
}

var askCmd = &cobra.Command{
	Use:   "ask [prompt]",
	Short: "Query the model through the Toxitoken gateway with real-time token streaming",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}

		var prompt string
		if len(args) > 0 {
			prompt = strings.Join(args, " ")
		} else {
			// If no prompt argument provided, read from stdin
			stdinBytes, err := io.ReadAll(os.Stdin)
			if err != nil {
				return fmt.Errorf("failed to read stdin: %w", err)
			}
			prompt = string(stdinBytes)
		}

		if strings.TrimSpace(prompt) == "" {
			return errors.New("empty prompt. Provide a prompt argument or pipe input via stdin")
		}

		return executeCompletion(cmd, cfg, prompt)
	},
}

var pipeCmd = &cobra.Command{
	Use:   "pipe [prompt]",
	Short: "Stream context from stdin through the Toxitoken gateway (e.g. cat main.go | toxi pipe 'Review')",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}

		stdinBytes, err := io.ReadAll(os.Stdin)
		if err != nil {
			return fmt.Errorf("failed to read stdin: %w", err)
		}

		prompt := string(stdinBytes)
		if len(args) > 0 {
			userQuery := strings.Join(args, " ")
			prompt = fmt.Sprintf("Context:\n```\n%s\n```\n\nPrompt: %s", prompt, userQuery)
		}

		if strings.TrimSpace(prompt) == "" {
			return errors.New("empty input. Pipe data via stdin or provide a prompt")
		}

		return executeCompletion(cmd, cfg, prompt)
	},
}

func executeCompletion(cmd *cobra.Command, cfg *CLIConfig, prompt string) error {

	modelFlag, _ := cmd.Flags().GetString("model")
	if modelFlag == "" {
		modelFlag = "gpt-4o-mini"
	}

	reqPayload := models.ChatCompletionRequest{
		Model: modelFlag,
		Messages: []models.Message{
			{Role: "user", Content: prompt},
		},
		Stream: true,
	}

	bodyBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return err
	}

	endpoint := fmt.Sprintf("%s/v1/chat/completions", cfg.GatewayURL)
	httpReq, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if cfg.Token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+cfg.Token)
	}

	// Inject mode headers
	if cfg.Mode == "mock" {
		httpReq.Header.Set("X-Proxy-Mode", "mock")
	}

	noCache, _ := cmd.Flags().GetBool("no-cache")
	if noCache {
		httpReq.Header.Set("Cache-Control", "no-cache")
	}

	if cfg.ChaosConfig != "" {
		httpReq.Header.Set("X-Chaos-Config", cfg.ChaosConfig)
	} else if cfg.Mode == "chaos" {
		httpReq.Header.Set("X-Chaos-Config", "rate=1.0,delay=100ms")
	}

	client := &http.Client{Timeout: 0}
	resp, err := client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("connection to gateway failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBytes, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("gateway error (HTTP %d): %s", resp.StatusCode, string(errBytes))
	}

	// Stream SSE chunks directly to os.Stdout
	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			trimmed := bytes.TrimSpace(line)
			if bytes.HasPrefix(trimmed, []byte("data: ")) {
				payload := bytes.TrimPrefix(trimmed, []byte("data: "))
				if bytes.Equal(payload, []byte("[DONE]")) {
					break
				}

				var chunk models.ChatCompletionChunk
				if jsonErr := json.Unmarshal(payload, &chunk); jsonErr == nil && len(chunk.Choices) > 0 {
					delta := chunk.Choices[0].Delta.Content
					if delta != "" {
						fmt.Print(delta)
						_ = os.Stdout.Sync()
					}
				}
			}
		}

		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			// Abrupt connection termination (e.g. chaos severing)
			fmt.Printf("\n[Stream severed by gateway/chaos: %v]\n", err)
			return nil
		}
	}

	fmt.Println()
	return nil
}

func init() {
	loginCmd.Flags().StringP("url", "u", "http://localhost:8080", "Toxitoken Gateway URL")
	loginCmd.Flags().StringP("token", "t", "", "Client API Token")
	loginCmd.Flags().StringP("mode", "m", "", "Gateway operating mode (live, mock, chaos)")
	loginCmd.Flags().String("chaos-config", "", "Chaos configuration (e.g. 'rate=1.0,delay=500ms,drop_after=5')")

	modeCmd.Flags().String("chaos-config", "", "Chaos configuration (e.g. 'rate=1.0,delay=500ms,drop_after=5')")

	askCmd.Flags().StringP("model", "m", "gpt-4o-mini", "Model identifier")
	askCmd.Flags().Bool("no-cache", false, "Bypass toxitoken caching")
	pipeCmd.Flags().StringP("model", "m", "gpt-4o-mini", "Model identifier")
	pipeCmd.Flags().Bool("no-cache", false, "Bypass toxitoken caching")

	rootCmd.AddCommand(loginCmd)
	rootCmd.AddCommand(modeCmd)
	rootCmd.AddCommand(statusCmd)
	rootCmd.AddCommand(askCmd)
	rootCmd.AddCommand(pipeCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
