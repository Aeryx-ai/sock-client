package client

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"

	"github.com/BurntSushi/toml"
)

// FileConfig is what the sock config file holds. Every key is optional.
type FileConfig struct {
	URL   string `toml:"url"`
	Token string `toml:"token"`
	// PuppetToken is the puppet token sock-mcp reads (sock puppets use writes
	// it). Token stays the human session token sock login writes.
	PuppetToken string `toml:"puppet_token"`
	// MachineKey is the machine key sock-mcp makes project puppets with
	// (sock machine-key use writes it).
	MachineKey string `toml:"machine_key"`
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
// file is an empty FileConfig, not an error. A file that holds a token, a
// machine key or an access gate and is readable by group or others is
// refused, naming the file and never its contents.
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
		return FileConfig{}, ParseError(path, err)
	}
	if (f.Token != "" || f.PuppetToken != "" || f.MachineKey != "" || f.AccessGate != "") && info.Mode().Perm()&0o077 != 0 {
		return FileConfig{}, fmt.Errorf("config: %s holds a secret but is readable by others (mode %o); run chmod 600 %s", path, info.Mode().Perm(), path)
	}
	return f, nil
}

// fileKeys are the keys a sock config file holds, the only ones an error
// may name: any other word on a line could be a pasted secret.
var fileKeys = map[string]bool{"url": true, "token": true, "puppet_token": true, "machine_key": true, "access_gate": true}

// tomlWhere finds the line and last key the parser reports.
var tomlWhere = regexp.MustCompile(`^toml: line (\d+)(?: \(last key "([^"]*)"\))?`)

// ParseError words a failure to decode the config file at path without
// quoting the file: the parser echoes the text it choked on, and an
// unquoted token or gate is that text. It names the line, and the key when
// it is one of sock's.
func ParseError(path string, err error) error {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return fmt.Errorf("config: %w", err)
	}
	msg := "config: parse " + path
	if m := tomlWhere.FindStringSubmatch(err.Error()); m != nil {
		msg += ": line " + m[1]
		if fileKeys[m[2]] {
			msg += " (key " + m[2] + ")"
		}
	}
	return errors.New(msg + ": not a valid sock config; quote string values")
}
