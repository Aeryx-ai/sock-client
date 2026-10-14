package client

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// FileConfig is what the sock config file holds. Every key is optional.
type FileConfig struct {
	URL        string `toml:"url"`
	Token      string `toml:"token"`
	AccessGate string `toml:"access_gate"`
}

// DefaultConfigPath is $XDG_CONFIG_HOME/sock/config.toml, else
// ~/.config/sock/config.toml.
func DefaultConfigPath(getenv func(string) string) (string, error) {
	if x := getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "sock", "config.toml"), nil
	}
	home := getenv("HOME")
	if home == "" {
		return "", errors.New("config: neither XDG_CONFIG_HOME nor HOME is set")
	}
	return filepath.Join(home, ".config", "sock", "config.toml"), nil
}

// LoadFileConfig reads the config file at path. An empty path or a missing
// file is an empty FileConfig, not an error. A file that holds a token or an
// access gate and is readable by group or others is refused, naming the file
// and never its contents.
//
// This reads the file only. How its keys combine with flags and the
// environment belongs to each caller: sock-cli takes flag, then env, then
// file per field; sock-mcp takes everything from the environment or
// everything from the file, so a file token never goes to an ambient URL.
func LoadFileConfig(path string) (FileConfig, error) {
	var f FileConfig
	if path == "" {
		return f, nil
	}
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, fmt.Errorf("config: %w", err)
	}
	if _, err := toml.DecodeFile(path, &f); err != nil {
		return f, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if (f.Token != "" || f.AccessGate != "") && info.Mode().Perm()&0o077 != 0 {
		return FileConfig{}, fmt.Errorf("config: %s holds a secret but is readable by others (mode %o); run chmod 600 %s", path, info.Mode().Perm(), path)
	}
	return f, nil
}
