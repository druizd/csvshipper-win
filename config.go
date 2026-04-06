package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type Config struct {
	SourceDir         string `json:"source_dir"`
	DoneDir           string `json:"done_dir"`
	ErrorDir          string `json:"error_dir"`
	WorkerCount       int    `json:"worker_count"`
	RabbitMQURL       string `json:"rabbitmq_url"`
	TaskQueue         string `json:"task_queue"`
	RPCTimeoutSeconds int    `json:"rpc_timeout_seconds"`
}

func LoadConfig(path string) (*Config, error) {
	if path == "" {
		// Attempt to load from current executable folder by default
		exePath, err := os.Executable()
		if err == nil {
			path = filepath.Join(filepath.Dir(exePath), "config.json")
		} else {
			path = "config.json"
		}
	}

	bytes, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := json.Unmarshal(bytes, &cfg); err != nil {
		return nil, err
	}

	// Apply defaults
	if cfg.WorkerCount <= 0 {
		cfg.WorkerCount = 1
	}
	if cfg.RPCTimeoutSeconds <= 0 {
		cfg.RPCTimeoutSeconds = 30
	}

	return &cfg, nil
}
