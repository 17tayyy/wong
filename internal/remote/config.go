package remote

import (
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Config is where this machine's wong server is and the token it uses.
type Config struct {
	URL   string `yaml:"url"`
	Token string `yaml:"token"`
}

func ConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "wong", "remote.yaml"), nil
}

// LoadConfig returns the machine's server, or ok false when none is set.
func LoadConfig(path string) (cfg Config, ok bool, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, false, nil
	}
	if err != nil {
		return cfg, false, err
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, false, fmt.Errorf("%s: %w", path, err)
	}
	if cfg.URL == "" || cfg.Token == "" {
		return cfg, false, nil
	}
	if err := CheckURL(cfg.URL); err != nil {
		return cfg, false, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, true, nil
}

func SaveConfig(path string, cfg Config) error {
	if err := CheckURL(cfg.URL); err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

var tailnet = netip.MustParsePrefix("100.64.0.0/10")

// CheckURL accepts https, and plain http only to a host on a private network, where the token does not cross the internet.
func CheckURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("invalid server url %q: use https://host or http://host:port", raw)
	}
	if u.Scheme == "https" || privateHost(u.Hostname()) {
		return nil
	}
	return fmt.Errorf("%s would send the token unencrypted over the internet: use https, or the server's address on your network or VPN", raw)
}

func privateHost(host string) bool {
	if ip, err := netip.ParseAddr(host); err == nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || tailnet.Contains(ip)
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "localhost" || !strings.Contains(host, ".") {
		return true
	}
	for _, suffix := range []string{".local", ".lan", ".internal", ".home.arpa", ".ts.net"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}
