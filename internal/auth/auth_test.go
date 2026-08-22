package auth

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// pgSecret is the secret that Postgres 16 stored for the password "app".
const pgSecret = "SCRAM-SHA-256$4096:fvBIIOneAN5o0os6Etn67Q==$+MebYzA7q3sjIvzuZXfvbh8Qp1EAFSkSTKvoXgK+IWM=:wXnGhWmwbLGyXC/2OKFrQZtQXBNm6FOW8VABtlmM2Us="

