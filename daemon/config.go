package daemon

import (
	"fmt"
	"github.com/goccy/go-yaml"
	"log"
	"os"
	"path/filepath"
	"regexp"
)

type Config struct {
	Address        string                     `yaml:"address"`
	Network        string                     `yaml:"network-group"`
	Keys           []string                   `yaml:"keys"`
	ServerKey      string                     `yaml:"server-key-file"`
	WorkspaceData  string                     `yaml:"workspace-parent"`
	GlobalShareDir string                     `yaml:"global-share-dir"`
	Templates      map[string]ContainerConfig `yaml:"templates"`
}

type ContainerConfig struct {
	EnableManager bool     `yaml:"enable-manager"`
	Image         string   `yaml:"image"`
	Exec          []string `yaml:"exec"`
	Cmd           []string `yaml:"cmd"`
	Env           []string `yaml:"env"`
	Volumes       []string `yaml:"volumes"`
	Privilege     bool     `yaml:"privilege"`
	Rm            bool     `yaml:"rm"`
}

func LoadConfig(path *string) (*Config, error) {
	//todo fill with defaults
	config := &Config{
		Address:        ":2233",
		Network:        "",
		ServerKey:      "id_rsa",
		WorkspaceData:  "",
		GlobalShareDir: "",
		Templates:      make(map[string]ContainerConfig),
	}
	file, err := os.Open(*path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	decoder := yaml.NewDecoder(file)
	if err := decoder.Decode(config); err != nil {
		log.Fatalf("Failed to parse config file: %v", err)
	}
	// clean path
	absPath, err := filepath.Abs(config.WorkspaceData)
	if err != nil {
		return nil, err
	}
	config.WorkspaceData = absPath

	absPath, err = filepath.Abs(config.GlobalShareDir)
	if err != nil {
		return nil, err
	}
	config.GlobalShareDir = absPath

	stat, err := os.Stat(config.WorkspaceData)
	if err != nil {
		if os.IsNotExist(err) {
			err = os.MkdirAll(config.WorkspaceData, 0600)
			if err != nil {
				return nil, fmt.Errorf("failed to create data directory: %v", err)
			}
		} else {
			return nil, err
		}
	}
	if !stat.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", config.WorkspaceData)
	}
	if config.WorkspaceData == "" {
		for _, containerConfig := range config.Templates {
			if containerConfig.EnableManager {
				return nil, fmt.Errorf("enable-manager depends on workspace-data feature.")
			}
		}
	}
	return config, nil
}

func (c *Config) GetTemplateByUser(user string) (*ContainerConfig, error) {
	for key, containerConfig := range c.Templates {
		matched, _ := regexp.Match(key, []byte(user))
		if matched {
			return &containerConfig, nil
		}
	}
	return nil, fmt.Errorf("cannot find template for user %v", user)
}
