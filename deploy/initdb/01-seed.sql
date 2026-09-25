-- Roles and databases for local development and CI.
CREATE ROLE app LOGIN PASSWORD 'app';
CREATE DATABASE appdb OWNER app;

-- bob is not in the userlist. pgmux finds his password with auth_query.
CREATE ROLE bob LOGIN PASSWORD 'bob';
GRANT CONNECT ON DATABASE appdb TO bob;

-- md5user stores an MD5 hash, to test MD5 auth to the server.
SET password_encryption = 'md5';
CREATE ROLE md5user LOGIN PASSWORD 'md5pass';
RESET password_encryption;
GRANT CONNECT ON DATABASE appdb TO md5user;

-- pgmux_auth runs auth_query. The function exposes password hashes only to that role.
CREATE ROLE pgmux_auth LOGIN PASSWORD 'pgmux_auth';
CREATE FUNCTION public.pgmux_lookup(p_user text, OUT usename name, OUT passwd text)
  RETURNS record LANGUAGE sql SECURITY DEFINER SET search_path = pg_catalog AS
  $$ SELECT usename, passwd FROM pg_shadow WHERE usename = p_user $$;
REVOKE ALL ON FUNCTION public.pgmux_lookup(text) FROM public;
GRANT EXECUTE ON FUNCTION public.pgmux_lookup(text) TO pgmux_auth;

\connect appdb
GRANT ALL ON SCHEMA public TO app;
CREATE TABLE public.items (id serial PRIMARY KEY, name text NOT NULL, created_at timestamptz DEFAULT now());
ALTER TABLE public.items OWNER TO app;
INSERT INTO public.items (name) SELECT 'item ' || g FROM generate_series(1, 100) g;
