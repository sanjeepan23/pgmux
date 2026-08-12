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

