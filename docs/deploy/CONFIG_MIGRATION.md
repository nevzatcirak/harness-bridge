# Migrating a host from the environment/drop-in deployment to the config file (ADR-027 section 5)

This is an **operator** procedure. The tools below only read, print and write the one file you name: no worker and no
command here edits a unit, runs `systemctl`, restarts a service or touches `/etc` for you. The live H5 host keeps
working with no edit until you choose to run these steps; `BRIDGE_*` is still accepted for one minor release (ADR-026).

Every command that reads a unit needs root, or a user that can read every `EnvironmentFile=` of it. Replace `cirak`
with the unit's name. Outputs name keys and files, never a value; a secret is `set` or `unset`.

## 1. Look, read-only

```sh
cirak doctor --from-unit cirak
```

This diagnoses the **unit's** environment (what `systemctl show` and its `EnvironmentFile=` files give), not your
shell. A bare `cirak doctor` is the shell-only first look and must not be used for this comparison. An unreadable,
unparseable or over-1-MiB environment file is an error that stops the command; a token delivered with `LoadCredential=`
is reported `UNVERIFIED` (`systemctl show` does not expose it; the credential is never read).

## 2. Import the unit's environment (not the shell)

```sh
cirak config import --from-unit cirak --out /etc/cirak/config.json
```

- If you cannot write `/etc/cirak`, the file is staged in a private directory and the exact `install` command for root
  is printed; run it, then remove the staging directory it names.
- An existing `--out` needs `--force` and gets a `0600` backup `config.json.bak-<UTC stamp>`.
- An inline `BRIDGE_TOKEN`/`CIRAK_TOKEN` or `OPENCODE_SERVER_PASSWORD` is **not copied**. The command prints
  `install -m 0600 /dev/null <path>`: run it, put the secret in the file with an editor (never on a command line), set
  `token.file` (or `harness.opencode.password_file`) in the config, or move the secret to `LoadCredential=`.
- `--reject-unknown` makes an unrecognised `CIRAK_*`/`BRIDGE_*` name an error; without it the names are warnings.

## 3. Compare, side by side

```sh
cirak config check --from-unit cirak --config /etc/cirak/config.json
cirak doctor --config /etc/cirak/config.json --from-unit cirak
```

The doctor prints a `compare:` table (the unit's value and the file's value for every key either sets). Proceed only
when it says the file and the source agree. `DIFFERENT` is a refusal and `ONLY IN THE SOURCE` is a warning: both mean
**do not remove the legacy lines yet**.

## 4. Point the unit at the file

Add `Environment=CIRAK_CONFIG=/etc/cirak/config.json` to the unit (or its drop-in), keep the legacy lines for one
restart, then `systemctl daemon-reload` and restart. Confirm `tools/list` and `cirak doctor --smoke` are unchanged. The
start logs the config path in use. While the file and the environment are equal, startup accepts them with one
warning per variable; a **difference** is a refusal, which is the intended guard.

## 5. Remove the legacy lines, and the rollback

Remove the migrated `BRIDGE_*`/`CIRAK_*` lines **only when the comparison of step 3 shows no difference**. After the
compatibility window the config file is the source.

**Rollback:** remove `Environment=CIRAK_CONFIG=...` (or the `EnvironmentFile=` line), restore the backup drop-in
(`zzzz-*.conf.bak-<timestamp>`), `systemctl daemon-reload`, restart. The legacy environment still works during the
window; the previous config file, if you replaced one, is `config.json.bak-<UTC stamp>`.

## Remote mode

`cirak init --remote --tls-cert ... --tls-key ... --public-origin https://host --principal name` writes a remote-mode
config (see [CONTRACTS](../CONTRACTS.md#remote-mode-adr-027-s4)). It never opens a firewall or edits a proxy; the proxy
steps stay yours: [Caddyfile.example](Caddyfile.example) and [PRIVATE_HTTPS_PREPARATION](PRIVATE_HTTPS_PREPARATION.md).

## Not decided here

The `Restart=` policy of the unit, the same-uid writer-lock directory requirement, and the default socket of
`cirak admin` against the daemon's effective configuration are maintainer questions; this procedure changes none of
them.
