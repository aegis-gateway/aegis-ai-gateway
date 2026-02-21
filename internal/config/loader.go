package config

import (
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"sync"

	"github.com/fsnotify/fsnotify"
	"gopkg.in/yaml.v3"
)

var envVarPattern = regexp.MustCompile(`\$\{([^}:]+)(?::([^}]*))?\}`)

// expandEnvVars replaces ${VAR} and ${VAR:default} patterns in a string.
func expandEnvVars(s string) string {
	return envVarPattern.ReplaceAllStringFunc(s, func(match string) string {
		submatch := envVarPattern.FindStringSubmatch(match)
		if len(submatch) < 2 {
			return match
		}
		varName := submatch[1]
		defaultVal := ""
		if len(submatch) >= 3 {
			defaultVal = submatch[2]
		}
		if val, ok := os.LookupEnv(varName); ok {
			return val
		}
		return defaultVal
	})
}

// LoadFile reads a YAML file, expands env vars, and unmarshals into dest.
func LoadFile(path string, dest interface{}) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read config file %s: %w", path, err)
	}
	expanded := expandEnvVars(string(data))
	if err := yaml.Unmarshal([]byte(expanded), dest); err != nil {
		return fmt.Errorf("parse config file %s: %w", path, err)
	}
	return nil
}

// Loader manages configuration loading and hot-reload via fsnotify.
type Loader struct {
	configDir string
	mu        sync.RWMutex
	cfg       *Config
	models    *ModelsConfig
	providers *ProvidersConfig
	watchers  []func()
	logger    *slog.Logger
}

func NewLoader(configDir string, logger *slog.Logger) *Loader {
	return &Loader{
		configDir: configDir,
		logger:    logger,
	}
}

func (l *Loader) Load() error {
	cfg := DefaultConfig()
	if err := LoadFile(l.configDir+"/gateway.yaml", cfg); err != nil {
		return fmt.Errorf("load gateway config: %w", err)
	}

	models := &ModelsConfig{}
	if err := LoadFile(l.configDir+"/models.yaml", models); err != nil {
		return fmt.Errorf("load models config: %w", err)
	}

	providers := &ProvidersConfig{}
	if err := LoadFile(l.configDir+"/providers.yaml", providers); err != nil {
		return fmt.Errorf("load providers config: %w", err)
	}

	l.mu.Lock()
	l.cfg = cfg
	l.models = models
	l.providers = providers
	l.mu.Unlock()

	l.logger.Info("configuration loaded", "dir", l.configDir)
	return nil
}

func (l *Loader) Config() *Config {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.cfg
}

func (l *Loader) Models() *ModelsConfig {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.models
}

func (l *Loader) Providers() *ProvidersConfig {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.providers
}

// OnReload registers a callback that fires after config is reloaded.
func (l *Loader) OnReload(fn func()) {
	l.watchers = append(l.watchers, fn)
}

// Watch starts watching the config directory for changes and reloads on modification.
func (l *Loader) Watch() error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("create fsnotify watcher: %w", err)
	}
	if err := watcher.Add(l.configDir); err != nil {
		watcher.Close()
		return fmt.Errorf("watch config dir %s: %w", l.configDir, err)
	}

	go func() {
		defer watcher.Close()
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) {
					l.logger.Info("config file changed, reloading", "file", event.Name)
					if err := l.Load(); err != nil {
						l.logger.Error("failed to reload config", "error", err)
						continue
					}
					for _, fn := range l.watchers {
						fn()
					}
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				l.logger.Error("fsnotify error", "error", err)
			}
		}
	}()

	return nil
}
