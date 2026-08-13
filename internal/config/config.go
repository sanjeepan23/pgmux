// Package config loads and watches the pgmux YAML configuration.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// Pool modes.
const (
	ModeSession     = "session"
	ModeTransaction = "transaction"
)

// Duration is a time.Duration that YAML reads from strings such as "30s".
type Duration struct{ time.Duration }

// UnmarshalYAML parses a Go duration string.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("line %d: %w", n.Line, err)
	}
	d.Duration = v
	return nil
}

// MarshalYAML writes the duration as a string.
func (d Duration) MarshalYAML() (any, error) { return d.String(), nil }

// Config is the complete pgmux configuration.
type Config struct {
	Listen          string              `yaml:"listen"`
	AdminListen     string              `yaml:"admin_listen"`
	AdminReadOnly   bool                `yaml:"admin_read_only"`
	DashboardDir    string              `yaml:"dashboard_dir"`
	PoolMode        string              `yaml:"pool_mode"`
	ShutdownTimeout Duration            `yaml:"shutdown_timeout"`
	MaxClientConn   int                 `yaml:"max_client_conn"`
	Server          Server              `yaml:"server"`
	ClientTLS       ClientTLS           `yaml:"client_tls"`
	Auth            Auth                `yaml:"auth"`
	Pool            Pool                `yaml:"pool"`
	Databases       map[string]Database `yaml:"databases"`
	Users           map[string]User     `yaml:"users"`

	// path is the file the config came from. Relative paths in the config resolve against its folder.
	path string
}

// Server describes the Postgres server behind the pooler.
type Server struct {
	Host           string   `yaml:"host"`
	Port           int      `yaml:"port"`
	SSLMode        string   `yaml:"sslmode"`
	SSLRootCert    string   `yaml:"sslrootcert"`
	ConnectTimeout Duration `yaml:"connect_timeout"`
}

// ClientTLS configures TLS for client connections.
type ClientTLS struct {
	CertFile string `yaml:"cert_file"`
	KeyFile  string `yaml:"key_file"`
	Required bool   `yaml:"required"`
}

// Enabled reports if the pooler accepts TLS from clients.
func (t ClientTLS) Enabled() bool { return t.CertFile != "" && t.KeyFile != "" }

// Auth configures client authentication.
type Auth struct {
	Method     string `yaml:"method"`
	Userlist   string `yaml:"userlist"`
	AuthUser   string `yaml:"auth_user"`
	AuthQuery  string `yaml:"auth_query"`
	AuthDBName string `yaml:"auth_dbname"`
}

