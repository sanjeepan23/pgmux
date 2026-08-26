package pool

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/sanjeepan23/pgmux/internal/auth"
	"github.com/sanjeepan23/pgmux/internal/protocol"
)

// Login holds what the pooler needs to log in to Postgres as one user.
type Login struct {
	User     string
	Database string
	Password string
	MD5      string
	Keys     *auth.ClientKeys
}

// Dialer opens connections to the Postgres server.
type Dialer struct {
	Addr           string
	SSLMode        string
	TLSConfig      *tls.Config
	ConnectTimeout time.Duration
}

// NewDialer builds a Dialer. For verify-ca and verify-full, rootCertFile is the CA bundle; empty means the system pool.
func NewDialer(host string, port int, sslMode, rootCertFile string, timeout time.Duration) (*Dialer, error) {
	d := &Dialer{Addr: net.JoinHostPort(host, strconv.Itoa(port)), SSLMode: sslMode, ConnectTimeout: timeout}
	if sslMode == "disable" {
		return d, nil
	}
	cfg := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	var roots *x509.CertPool
	if rootCertFile != "" {
		pem, err := os.ReadFile(rootCertFile)
		if err != nil {
			return nil, fmt.Errorf("read sslrootcert: %w", err)
		}
		roots = x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("sslrootcert has no valid certificates")
		}
		cfg.RootCAs = roots
	}
	switch sslMode {
	case "prefer", "require":
		cfg.InsecureSkipVerify = true
	case "verify-ca":
		// Check the chain but not the host name, like libpq.
		cfg.InsecureSkipVerify = true
		cfg.VerifyPeerCertificate = func(raw [][]byte, _ [][]*x509.Certificate) error {
			certs := make([]*x509.Certificate, len(raw))
			for i, r := range raw {
				c, err := x509.ParseCertificate(r)
				if err != nil {
					return err
				}
				certs[i] = c
			}
			if len(certs) == 0 {
				return errors.New("server sent no certificate")
			}
			opts := x509.VerifyOptions{Roots: roots, Intermediates: x509.NewCertPool()}
			for _, c := range certs[1:] {
				opts.Intermediates.AddCert(c)
			}
			_, err := certs[0].Verify(opts)
			return err
		}
	}
	d.TLSConfig = cfg
	return d, nil
}

func (d *Dialer) dialTransport(ctx context.Context) (net.Conn, error) {
	nd := net.Dialer{Timeout: d.ConnectTimeout, KeepAlive: 30 * time.Second}
	nc, err := nd.DialContext(ctx, "tcp", d.Addr)
	if err != nil {
		return nil, err
	}
	if d.SSLMode == "disable" {
		return nc, nil
	}
	var req [8]byte
	binary.BigEndian.PutUint32(req[0:], 8)
	binary.BigEndian.PutUint32(req[4:], protocol.SSLRequestCode)
	if _, err := nc.Write(req[:]); err != nil {
		nc.Close()
		return nil, err
	}
	var resp [1]byte
	if _, err := nc.Read(resp[:]); err != nil {
		nc.Close()
		return nil, err
	}
	if resp[0] != 'S' {
		if d.SSLMode == "prefer" {
			return nc, nil
		}
		nc.Close()
		return nil, errors.New("server does not support TLS")
	}
	tc := tls.Client(nc, d.TLSConfig)
	if err := tc.HandshakeContext(ctx); err != nil {
		nc.Close()
		return nil, fmt.Errorf("tls handshake: %w", err)
	}
	return tc, nil
}

// Dial opens and authenticates a new server connection.
func (d *Dialer) Dial(ctx context.Context, login Login) (*Conn, error) {
	if d.ConnectTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, d.ConnectTimeout)
		defer cancel()
	}
	nc, err := d.dialTransport(ctx)
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = nc.SetDeadline(dl)
	}
	c, err := startup(nc, login)
	if err != nil {
		nc.Close()
		return nil, err
	}
	_ = nc.SetDeadline(time.Time{})
	return c, nil
}

func startup(nc net.Conn, login Login) (*Conn, error) {
	rd := protocol.NewReader(nc, 0)
	wr := protocol.NewWriter(nc)
	err := wr.Write(&pgproto3.StartupMessage{
		ProtocolVersion: protocol.ProtocolVersion30,
		Parameters:      map[string]string{"user": login.User, "database": login.Database},
	})
	if err == nil {
		err = wr.Flush()
	}
	if err != nil {
		return nil, err
	}
	params := map[string]string{}
	var pid uint32
	var secret []byte
	var scram *auth.ScramClient
	for {
		m, err := rd.ReadMessage()
		if err != nil {
			return nil, err
		}
		switch m.Type {
		case 'R':
			if scram, err = handleAuth(wr, m.Body, login, scram); err != nil {
				return nil, err
			}
		case 'S':
			var ps pgproto3.ParameterStatus
			if err := ps.Decode(m.Body); err != nil {
				return nil, err
			}
			params[ps.Name] = ps.Value
		case 'K':
			var kd pgproto3.BackendKeyData
			if err := kd.Decode(m.Body); err != nil {
				return nil, err
			}
			pid, secret = kd.ProcessID, kd.SecretKey
		case 'E':
			return nil, protocol.DecodeError(m.Body)
		case 'Z':
			return newConn(nc, rd, params, pid, secret), nil
		case 'N', 'v':
			// Notices and NegotiateProtocolVersion need no action.
		default:
			return nil, fmt.Errorf("unexpected message %q during server startup", m.Type)
		}
	}
}

func handleAuth(wr *protocol.Writer, body []byte, login Login, scram *auth.ScramClient) (*auth.ScramClient, error) {
	if len(body) < 4 {
		return nil, errors.New("short authentication message")
	}
	var out pgproto3.FrontendMessage
	switch kind := binary.BigEndian.Uint32(body); kind {
	case 0: // AuthenticationOk
		return scram, nil
	case 3: // AuthenticationCleartextPassword
		if login.Password == "" {
			return nil, errors.New("server wants a cleartext password, but pgmux has none for this user")
		}
		out = &pgproto3.PasswordMessage{Password: login.Password}
	case 5: // AuthenticationMD5Password
		if login.MD5 == "" {
			return nil, errors.New("server wants an MD5 password, but pgmux has no password or MD5 hash for this user")
		}
		out = &pgproto3.PasswordMessage{Password: auth.MD5Response(login.MD5, body[4:8])}
	case 10: // AuthenticationSASL
		switch {
		case login.Password != "":
			scram = auth.NewScramClient(login.Password)
		case login.Keys != nil:
			scram = auth.NewScramClientWithKeys(login.Keys.ClientKey, login.Keys.ServerKey)
		default:
			return nil, errors.New("server wants SCRAM, but pgmux has no password or client keys for this user yet")
		}
		out = &pgproto3.SASLInitialResponse{AuthMechanism: scram.Mechanism(), Data: scram.First()}
	case 11: // AuthenticationSASLContinue
		if scram == nil {
			return nil, errors.New("unexpected SASL continue")
		}
		data, err := scram.Final(body[4:])
		if err != nil {
			return nil, err
		}
		out = &pgproto3.SASLResponse{Data: data}
	case 12: // AuthenticationSASLFinal
		if scram == nil {
			return nil, errors.New("unexpected SASL final")
		}
		return scram, scram.Verify(body[4:])
	default:
		return nil, fmt.Errorf("unsupported server authentication method %d", kind)
	}
	if err := wr.Write(out); err != nil {
		return nil, err
	}
	return scram, wr.Flush()
}

