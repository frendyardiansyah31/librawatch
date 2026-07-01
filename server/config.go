package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server struct {
		Port int    `yaml:"port"`
		Host string `yaml:"host"`
	} `yaml:"server"`

	Auth struct {
		Token         string   `yaml:"token"`          // WebSocket token for agents (empty = disabled)
		AdminCIDRs    []string `yaml:"admin_cidrs"`    // IP ranges allowed to access dashboard/API (empty = all)
		AdminUsername string   `yaml:"admin_username"` // dashboard login username (empty = auth disabled)
		AdminPassword string   `yaml:"admin_password"` // dashboard login password
	} `yaml:"auth"`

	Database struct {
		Path string `yaml:"path"`
	} `yaml:"database"`

	Alerts struct {
		CPUThreshold        float64  `yaml:"cpu_threshold"`
		RAMThreshold        float64  `yaml:"ram_threshold"`
		OfflineAfterMinutes int      `yaml:"offline_after_minutes"`
		Blacklist           []string `yaml:"blacklist"`
	} `yaml:"alerts"`

	Telegram struct {
		Token  string `yaml:"token"`
		ChatID string `yaml:"chat_id"`
	} `yaml:"telegram"`

	Email struct {
		SMTPHost string `yaml:"smtp_host"`
		SMTPPort int    `yaml:"smtp_port"`
		SMTPUser string `yaml:"smtp_user"`
		SMTPPass string `yaml:"smtp_pass"`
		SMTPTo   string `yaml:"smtp_to"`
	} `yaml:"email"`

	MeshCentral struct {
		URL string `yaml:"url"`
	} `yaml:"meshcentral"`

	Uploads struct {
		Path      string `yaml:"path"`
		MaxSizeMB int64  `yaml:"max_size_mb"`
	} `yaml:"uploads"`
}

const defaultConfigYAML = `server:
  port: 8080
  host: "0.0.0.0"

auth:
  token: ""             # token untuk agent WebSocket (kosong = nonaktif)
  admin_cidrs: []       # batasi akses dashboard/API ke IP tertentu, contoh: ["10.5.39.88/32"]
  admin_username: ""    # username login dashboard (kosong = auth nonaktif)
  admin_password: ""    # password login dashboard

database:
  path: "./data/library.db"

alerts:
  cpu_threshold: 85
  ram_threshold: 85
  offline_after_minutes: 5
  blacklist:
    - "steam.exe"
    - "epicgameslauncher.exe"
    - "discord.exe"
    - "battle.net.exe"
    - "leagueoflegends.exe"

telegram:
  token: ""
  chat_id: ""

email:
  smtp_host: ""
  smtp_port: 587
  smtp_user: ""
  smtp_pass: ""
  smtp_to: ""

meshcentral:
  url: "http://192.168.1.10:4430"

uploads:
  path: "./uploads"
  max_size_mb: 500
`

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("read config: %w", err)
		}
		if werr := os.WriteFile(path, []byte(defaultConfigYAML), 0644); werr != nil {
			return nil, fmt.Errorf("create default config: %w", werr)
		}
		fmt.Printf("config.yaml not found — created default at %s\nEdit it and restart the server.\n", path)
		data = []byte(defaultConfigYAML)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8080
	}
	if cfg.Server.Host == "" {
		cfg.Server.Host = "0.0.0.0"
	}
	if cfg.Database.Path == "" {
		cfg.Database.Path = "./data/library.db"
	}
	if cfg.Alerts.CPUThreshold == 0 {
		cfg.Alerts.CPUThreshold = 85
	}
	if cfg.Alerts.RAMThreshold == 0 {
		cfg.Alerts.RAMThreshold = 85
	}
	if cfg.Alerts.OfflineAfterMinutes == 0 {
		cfg.Alerts.OfflineAfterMinutes = 5
	}
	if cfg.Uploads.Path == "" {
		cfg.Uploads.Path = "./uploads"
	}
	if cfg.Uploads.MaxSizeMB == 0 {
		cfg.Uploads.MaxSizeMB = 500
	}
	if cfg.MeshCentral.URL == "" {
		cfg.MeshCentral.URL = "http://192.168.1.10:4430"
	}
	if cfg.Email.SMTPPort == 0 {
		cfg.Email.SMTPPort = 587
	}

	return &cfg, nil
}
